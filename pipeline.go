package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"autopack/utils"
)

// UploadBatcher is the upload boundary used by the processing pipeline.
// AssetUploader implements it, while tests and future frontends can provide
// their own implementation without handling Roblox credentials here.
type UploadBatcher interface {
	UploadMany(context.Context, []utils.UploadRequest) []utils.UploadResult
}

// PipelineProgress receives one final event for each prepared upload.
type PipelineProgress func(done, total int, name string, err error)

// PipelineLog receives sanitized, user-facing messages for completed pipeline
// milestones. It never receives credentials or temporary filesystem paths.
type PipelineLog func(message string)

// PipelineResult is the strict JSON object consumed by the target game.
type PipelineResult struct {
	Values     map[string]any
	PackID     string
	PackName   string
	PreviewPNG []byte
}

type textureBinding struct {
	SourceKey     string
	TextureFields []string
	VPFields      []string
}

type meshBinding struct {
	Name       string
	SourceKeys []string
	JSONFields []string
	Config     utils.Config
}

type preparedUpload struct {
	Request    utils.UploadRequest
	JSONFields []string
}

type cachedTexture struct {
	Resized  *image.NRGBA
	Expanded *image.NRGBA
}

// preparationConcurrency keeps every CPU core busy without allowing machines
// with very large core counts to allocate an excessive number of simultaneous
// 512x512 edge-expansion workspaces.
func preparationConcurrency() int {
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		return 1
	}
	if workers > 32 {
		return 32
	}
	return workers
}

// Uploads are mostly network waits, so using more workers than CPU-bound image
// preparation is useful. The cap prevents a large machine from flooding the
// Roblox endpoint with all assets at once.
func uploadConcurrency() int {
	workers := runtime.GOMAXPROCS(0) * 2
	if workers < 16 {
		workers = 16
	}
	if workers > 32 {
		workers = 32
	}
	return workers
}

// JSON field mapping from Minecraft 1.8.9 texture keys. Iron tools deliberately
// populate the game's Gold fields, matching Example_Texturepack.jsonc.
var pipelineTextures = []textureBinding{
	{SourceKey: "stone_sword", TextureFields: []string{"SwordTexture"}, VPFields: []string{"SwordVPImage"}},
	{SourceKey: "diamond_sword", TextureFields: []string{"DiamondSwordTexture"}, VPFields: []string{"DiamondSwordVPImage"}},
	{SourceKey: "iron_sword", TextureFields: []string{"GoldSwordTexture"}, VPFields: []string{"GoldSwordVPImage"}},
	{SourceKey: "wood_sword", TextureFields: []string{"WoodenSwordTexture"}, VPFields: []string{"WoodenSwordVPImage"}},

	{SourceKey: "stone_pickaxe", TextureFields: []string{"PickaxeTexture"}, VPFields: []string{"PickaxeVPImage"}},
	{SourceKey: "diamond_pickaxe", TextureFields: []string{"DiamondPickaxeTexture"}, VPFields: []string{"DiamondPickaxeVPImage"}},
	{SourceKey: "iron_pickaxe", TextureFields: []string{"GoldPickaxeTexture"}, VPFields: []string{"GoldPickaxeVPImage"}},
	{SourceKey: "wood_pickaxe", TextureFields: []string{"WoodenPickaxeTexture"}, VPFields: []string{"WoodenPickaxeVPImage"}},

	{SourceKey: "stone_axe", TextureFields: []string{"AxeTexture"}, VPFields: []string{"AxeVPImage"}},
	{SourceKey: "diamond_axe", TextureFields: []string{"DiamondAxeTexture"}, VPFields: []string{"DiamondAxeVPImage"}},
	{SourceKey: "iron_axe", TextureFields: []string{"GoldAxeTexture"}, VPFields: []string{"GoldAxeVPImage"}},
	{SourceKey: "wood_axe", TextureFields: []string{"WoodenAxeTexture"}, VPFields: []string{"WoodenAxeVPImage"}},

	{SourceKey: "bow_standby", TextureFields: []string{"Bow0Texture"}, VPFields: []string{"DefaultBowVPImage"}},
	{SourceKey: "bow_pulling_0", TextureFields: []string{"Bow1Texture"}},
	{SourceKey: "bow_pulling_1", TextureFields: []string{"Bow2Texture"}},
	{SourceKey: "bow_pulling_2", TextureFields: []string{"Bow3Texture"}},

	{SourceKey: "apple_golden", TextureFields: []string{"GoldAppleTexture"}, VPFields: []string{"GoldAppleVPImage"}},
	{SourceKey: "iron_ingot", TextureFields: []string{"IronTexture"}, VPFields: []string{"IronVPImage"}},
	{SourceKey: "diamond", TextureFields: []string{"DiamondTexture"}, VPFields: []string{"DiamondVPImage"}},
	{SourceKey: "emerald", TextureFields: []string{"EmeraldTexture"}, VPFields: []string{"EmeraldVPImage"}},
	{SourceKey: "ender_pearl", TextureFields: []string{"PearlTexture"}, VPFields: []string{"PearlVPImage"}},
	{SourceKey: "shears", TextureFields: []string{"ShearsTexture"}, VPFields: []string{"ShearsVPImage"}},
	{SourceKey: "fireball", TextureFields: []string{"FireballTexture"}, VPFields: []string{"FireballVPImage"}},
	{SourceKey: "jump_pot", TextureFields: []string{"JumpPotionTexture"}, VPFields: []string{"JumpPotionVPImage"}},
	{SourceKey: "speed_pot", TextureFields: []string{"SpeedPotionTexture"}, VPFields: []string{"SpeedPotionVPImage"}},

	// The game calls these Clay fields, but they intentionally use wool.
	{SourceKey: "wool_blue", TextureFields: []string{"ClayBlue"}},
	{SourceKey: "wool_cyan", TextureFields: []string{"ClayCyan"}},
	{SourceKey: "wool_green", TextureFields: []string{"ClayGreen"}},
	{SourceKey: "wool_gray", TextureFields: []string{"ClayGrey"}},
	{SourceKey: "wool_orange", TextureFields: []string{"ClayOrange"}},
	{SourceKey: "wool_purple", TextureFields: []string{"ClayPurple"}},
	{SourceKey: "wool_red", TextureFields: []string{"ClayRed"}},
	{SourceKey: "wool_white", TextureFields: []string{"ClayWhite"}},
	{SourceKey: "wool_yellow", TextureFields: []string{"ClayYellow"}},
}

func pipelineMeshes() []meshBinding {
	standard := utils.DefaultConfig()
	// The target game treats the opposite horizontal direction as north.
	// Blender Z maps to Roblox Y, so adding 180 degrees here flips every item
	// around Roblox's vertical axis without changing its size or handedness.
	standard.RotateZ += 180
	flat := standard
	flat.RotateY = 0
	// Keep the sword tilt, then yaw the axe by another half turn. This changes
	// which direction its head faces without rolling it upside down.
	axe := standard
	axe.RotateZ += 180
	return []meshBinding{
		{Name: "sword", SourceKeys: []string{"stone_sword", "diamond_sword", "iron_sword", "wood_sword"}, JSONFields: []string{"SwordMesh"}, Config: standard},
		{Name: "pickaxe", SourceKeys: []string{"stone_pickaxe", "diamond_pickaxe", "iron_pickaxe", "wood_pickaxe"}, JSONFields: []string{"PickaxeMesh"}, Config: standard},
		{Name: "axe", SourceKeys: []string{"stone_axe", "diamond_axe", "iron_axe", "wood_axe"}, JSONFields: []string{"AxeMesh"}, Config: axe},
		{Name: "bow_0", SourceKeys: []string{"bow_standby"}, JSONFields: []string{"Bow0Mesh"}, Config: standard},
		{Name: "bow_1", SourceKeys: []string{"bow_pulling_0"}, JSONFields: []string{"Bow1Mesh"}, Config: standard},
		{Name: "bow_2", SourceKeys: []string{"bow_pulling_1"}, JSONFields: []string{"Bow2Mesh"}, Config: standard},
		{Name: "bow_3", SourceKeys: []string{"bow_pulling_2"}, JSONFields: []string{"Bow3Mesh"}, Config: standard},
		{Name: "gold_apple", SourceKeys: []string{"apple_golden"}, JSONFields: []string{"GoldAppleMesh"}, Config: flat},
		{Name: "iron", SourceKeys: []string{"iron_ingot"}, JSONFields: []string{"IronMesh"}, Config: flat},
		{Name: "diamond", SourceKeys: []string{"diamond"}, JSONFields: []string{"DiamondMesh"}, Config: flat},
		{Name: "emerald", SourceKeys: []string{"emerald"}, JSONFields: []string{"EmeraldMesh"}, Config: flat},
		{Name: "pearl", SourceKeys: []string{"ender_pearl"}, JSONFields: []string{"PearlMesh"}, Config: flat},
		{Name: "shears", SourceKeys: []string{"shears"}, JSONFields: []string{"ShearsMesh"}, Config: standard},
		// Like potions, the fireball is displayed by a view model that expects an
		// upright flat mesh; the standard tool yaw tips it onto its side.
		{Name: "fireball", SourceKeys: []string{"fireball"}, JSONFields: []string{"FireballMesh"}, Config: flat},
		// Potions must remain vertically upright in the target view model. The
		// standard -45-degree yaw makes their bottle axis lie on its side.
		{Name: "potion", SourceKeys: []string{"jump_pot", "speed_pot"}, JSONFields: []string{"JumpPotionMesh", "SpeedPotionMesh"}, Config: flat},
	}
}

// RunPipeline extracts, prepares, meshes, uploads, and maps one texture pack.
// It never writes credentials or intermediate files outside its private temp
// folder, which is removed before this function returns.
func RunPipeline(ctx context.Context, zipPath string, uploader UploadBatcher, progress PipelineProgress) (PipelineResult, error) {
	return runPipeline(ctx, zipPath, uploader, progress, nil)
}

func runPipeline(ctx context.Context, zipPath string, uploader UploadBatcher, progress PipelineProgress, log PipelineLog) (PipelineResult, error) {
	if ctx == nil {
		return PipelineResult{}, errors.New("pipeline context is nil")
	}
	if uploader == nil {
		return PipelineResult{}, errors.New("upload client is nil")
	}
	logPipeline(log, "Reading Minecraft texture pack")
	pack, err := utils.UnzipTexturePack(zipPath)
	if err != nil {
		return PipelineResult{}, err
	}
	defer pack.Cleanup()
	if missing := requiredMissingTextures(pack.Textures); len(missing) != 0 {
		return PipelineResult{}, fmt.Errorf("texture pack is missing required textures: %s", strings.Join(missing, ", "))
	}
	logPipeline(log, fmt.Sprintf("Found %d mapped textures", len(pack.Textures)))
	previewPNG, err := buildHotbarPreview(pack.Textures)
	if err != nil {
		return PipelineResult{}, err
	}
	logPipeline(log, "Rendered hotbar preview")

	workDir := filepath.Join(pack.TempDir, "autopack_pipeline")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return PipelineResult{}, fmt.Errorf("create pipeline work directory: %w", err)
	}
	prepared, err := preparePipelineAssets(ctx, pack.Textures, workDir, log)
	if err != nil {
		return PipelineResult{}, err
	}
	requests := make([]utils.UploadRequest, len(prepared))
	for index := range prepared {
		requests[index] = prepared[index].Request
	}
	logPipeline(log, fmt.Sprintf("Uploading %d assets to Roblox", len(requests)))
	results, streamed := uploadPipelineAssets(ctx, uploader, requests, progress)
	if len(results) != len(prepared) {
		return PipelineResult{}, fmt.Errorf("upload client returned %d results for %d requests", len(results), len(prepared))
	}
	values, err := defaultPipelineValues()
	if err != nil {
		return PipelineResult{}, err
	}
	accepted := 0
	skipped := 0
	for index, result := range results {
		var resultErr error
		if result.Error != "" {
			resultErr = errors.New(result.Error)
		} else if result.Asset == nil || result.Asset.AssetID == "" {
			resultErr = errors.New("upload returned no asset ID")
		} else {
			accepted++
			for _, field := range prepared[index].JSONFields {
				values[field] = result.Asset.AssetID
			}
		}
		if resultErr != nil {
			skipped++
			logPipeline(log, fmt.Sprintf(
				"Skipped %s; using default for %s. Roblox error: %s",
				prepared[index].Request.DisplayName,
				strings.Join(prepared[index].JSONFields, ", "),
				resultErr,
			))
		}
		if progress != nil && !streamed {
			progress(index+1, len(results), prepared[index].Request.DisplayName, resultErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return PipelineResult{}, err
	}
	if skipped == 0 {
		logPipeline(log, fmt.Sprintf("Roblox accepted all %d assets and applied permissions", accepted))
	} else {
		logPipeline(log, fmt.Sprintf(
			"Roblox accepted %d assets; skipped %d and kept their default asset IDs",
			accepted, skipped,
		))
	}
	return PipelineResult{Values: values, PreviewPNG: previewPNG}, nil
}

type streamingUploadBatcher interface {
	UploadManyWithProgress(context.Context, []utils.UploadRequest, func(index int, result utils.UploadResult)) []utils.UploadResult
}

func uploadPipelineAssets(ctx context.Context, uploader UploadBatcher, requests []utils.UploadRequest, progress PipelineProgress) ([]utils.UploadResult, bool) {
	streaming, ok := uploader.(streamingUploadBatcher)
	if !ok {
		return uploader.UploadMany(ctx, requests), false
	}
	completed := 0
	var callbackMu sync.Mutex
	results := streaming.UploadManyWithProgress(ctx, requests, func(_ int, result utils.UploadResult) {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		completed++
		if progress == nil {
			return
		}
		var resultErr error
		if result.Error != "" {
			resultErr = errors.New(result.Error)
		} else if result.Asset == nil || result.Asset.AssetID == "" {
			resultErr = errors.New("upload returned no asset ID")
		}
		progress(completed, len(requests), result.Request.DisplayName, resultErr)
	})
	return results, true
}

func logPipeline(log PipelineLog, message string) {
	if log != nil {
		log(message)
	}
}

func requiredMissingTextures(textures map[string]string) []string {
	required := make(map[string]struct{})
	for _, binding := range pipelineTextures {
		required[binding.SourceKey] = struct{}{}
	}
	for _, binding := range pipelineMeshes() {
		for _, sourceKey := range binding.SourceKeys {
			required[sourceKey] = struct{}{}
		}
	}
	var missing []string
	for key := range required {
		if textures[key] == "" {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

func preparePipelineAssets(ctx context.Context, textures map[string]string, workDir string, log PipelineLog) ([]preparedUpload, error) {
	cache, err := cachePipelineTextures(ctx, textures, log)
	if err != nil {
		return nil, err
	}

	// Keep the old deterministic upload order while letting every independent
	// PNG and mesh preparation job execute through the same CPU-sized pool.
	type assetJob func() (preparedUpload, error)
	jobs := make([]assetJob, 0, len(pipelineTextures)*2+len(pipelineMeshes()))
	for _, binding := range pipelineTextures {
		binding := binding
		if len(binding.TextureFields) != 0 {
			jobs = append(jobs, func() (preparedUpload, error) {
				return prepareTextureUpload(cache[binding.SourceKey].Expanded, binding, false, workDir)
			})
		}
		if len(binding.VPFields) != 0 {
			jobs = append(jobs, func() (preparedUpload, error) {
				return prepareTextureUpload(cache[binding.SourceKey].Resized, binding, true, workDir)
			})
		}
	}
	for _, binding := range pipelineMeshes() {
		binding := binding
		jobs = append(jobs, func() (preparedUpload, error) {
			return prepareMeshBinding(cache, binding, workDir)
		})
	}
	imageCount := len(jobs) - len(pipelineMeshes())
	logPipeline(log, fmt.Sprintf("Encoding %d images and %d greedy meshes", imageCount, len(pipelineMeshes())))

	prepared := make([]preparedUpload, len(jobs))
	err = parallelFor(ctx, len(jobs), preparationConcurrency(), func(index int) error {
		upload, err := jobs[index]()
		if err == nil {
			prepared[index] = upload
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	logPipeline(log, fmt.Sprintf("Prepared %d upload files", len(prepared)))
	return prepared, nil
}

func cachePipelineTextures(ctx context.Context, textures map[string]string, log PipelineLog) (map[string]cachedTexture, error) {
	required := make(map[string]bool)
	for _, binding := range pipelineTextures {
		required[binding.SourceKey] = len(binding.TextureFields) != 0
	}
	for _, binding := range pipelineMeshes() {
		for _, sourceKey := range binding.SourceKeys {
			if _, exists := required[sourceKey]; !exists {
				required[sourceKey] = false
			}
		}
	}
	keys := make([]string, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]cachedTexture, len(keys))
	logPipeline(log, fmt.Sprintf("Decoding and resizing %d textures", len(keys)))
	if err := parallelFor(ctx, len(keys), preparationConcurrency(), func(index int) error {
		key := keys[index]
		file, err := os.Open(textures[key])
		if err != nil {
			return fmt.Errorf("open texture %q: %w", key, err)
		}
		img, decodeErr := png.Decode(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return fmt.Errorf("decode texture %q: %w", key, decodeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close texture %q: %w", key, closeErr)
		}
		entries[index].Resized = utils.ResizeTexture(img)
		return nil
	}); err != nil {
		return nil, err
	}
	logPipeline(log, fmt.Sprintf("Resized %d textures to 512x512", len(keys)))

	// Expansion is the most expensive image operation. Run it on all cores only
	// once per source and share the immutable result with PNG and GLB encoders.
	expandedCount := 0
	for _, expands := range required {
		if expands {
			expandedCount++
		}
	}
	logPipeline(log, fmt.Sprintf("Edge-expanding %d textures", expandedCount))
	if err := parallelFor(ctx, len(keys), preparationConcurrency(), func(index int) error {
		if required[keys[index]] {
			entries[index].Expanded = utils.EdgeExpand(entries[index].Resized)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	logPipeline(log, fmt.Sprintf("Edge-expanded %d textures", expandedCount))
	cache := make(map[string]cachedTexture, len(keys))
	for index, key := range keys {
		cache[key] = entries[index]
	}
	return cache, nil
}

func prepareTextureUpload(img image.Image, binding textureBinding, vpImage bool, workDir string) (preparedUpload, error) {
	suffix := "_texture.png"
	displaySuffix := " texture"
	fields := binding.TextureFields
	if vpImage {
		suffix = "_vp.png"
		displaySuffix = " VPImage"
		fields = binding.VPFields
	}
	path := filepath.Join(workDir, binding.SourceKey+suffix)
	if err := writePNG(path, img); err != nil {
		return preparedUpload{}, err
	}
	return preparedUpload{
		Request: utils.UploadRequest{
			FilePath: path, DisplayName: "Cone " + binding.SourceKey + displaySuffix,
			AssetType: utils.AssetTypeImage,
		},
		JSONFields: fields,
	}, nil
}

func prepareMeshBinding(textures map[string]cachedTexture, binding meshBinding, workDir string) (preparedUpload, error) {
	images := make([]image.Image, 0, len(binding.SourceKeys))
	for _, sourceKey := range binding.SourceKeys {
		images = append(images, textures[sourceKey].Resized)
	}
	union := unionAlpha(images)
	meshConfig := binding.Config
	meshConfig.AlphaThreshold = utils.DetectMeshAlphaThreshold(union)
	mesh, _, err := utils.BuildGreedyMesh(union, meshConfig)
	if err != nil {
		return preparedUpload{}, fmt.Errorf("build %s mesh: %w", binding.Name, err)
	}
	// Keep one union mesh for every material variant. Roblox's GLB endpoint
	// creates a Model package ID, which cannot be assigned to MeshPart.MeshId.
	// Use Roblox's native stream for upload; utils.EncodeGLB remains the
	// portable library export.
	robloxMesh, err := utils.EncodeRobloxMesh(mesh)
	if err != nil {
		return preparedUpload{}, fmt.Errorf("encode %s Roblox mesh: %w", binding.Name, err)
	}
	path := filepath.Join(workDir, binding.Name+".mesh")
	if err := os.WriteFile(path, robloxMesh, 0o644); err != nil {
		return preparedUpload{}, fmt.Errorf("write %s Roblox mesh: %w", binding.Name, err)
	}
	return preparedUpload{
		Request:    utils.UploadRequest{FilePath: path, DisplayName: "Cone " + binding.Name + " mesh", AssetType: utils.AssetTypeMesh},
		JSONFields: binding.JSONFields,
	}, nil
}

func unionAlpha(images []image.Image) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, utils.EdgeExpandedTextureSize, utils.EdgeExpandedTextureSize))
	for _, img := range images {
		for y := 0; y < result.Bounds().Dy(); y++ {
			for x := 0; x < result.Bounds().Dx(); x++ {
				pixel := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
				offset := result.PixOffset(x, y)
				if pixel.A <= result.Pix[offset+3] {
					continue
				}
				result.Pix[offset] = 255
				result.Pix[offset+1] = 255
				result.Pix[offset+2] = 255
				result.Pix[offset+3] = pixel.A
			}
		}
	}
	return result
}

func writePNG(path string, img image.Image) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create prepared PNG %q: %w", path, err)
	}
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(file, img); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode prepared PNG %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close prepared PNG %q: %w", path, err)
	}
	return nil
}

func parallelFor(ctx context.Context, jobs, workers int, run func(int) error) error {
	if jobs == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if workers < 1 {
		workers = 1
	}
	if workers > jobs {
		workers = jobs
	}
	workContext, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan int, jobs)
	for index := 0; index < jobs; index++ {
		queue <- index
	}
	close(queue)

	var firstErr error
	var errorOnce sync.Once
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for {
				select {
				case <-workContext.Done():
					return
				case index, ok := <-queue:
					if !ok {
						return
					}
					if err := run(index); err != nil {
						errorOnce.Do(func() {
							firstErr = err
							cancel()
						})
						return
					}
				}
			}
		}()
	}
	wait.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func writeExportJSON(path string, output PipelineResult) error {
	data, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode output JSON: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write output JSON %q: %w", path, err)
	}
	return nil
}

// MarshalJSON emits the buffer envelope consumed by the target game. The
// zbase64 payload is a zstd-compressed JSON object containing Values.
func (output PipelineResult) MarshalJSON() ([]byte, error) {
	return encodeCompressedJSON(output.Values)
}

func availableOutputPath(zipPath string) string {
	directory := filepath.Dir(zipPath)
	stem := strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))
	base := filepath.Join(directory, stem+"_cone.json")
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for number := 2; ; number++ {
		candidate := filepath.Join(directory, fmt.Sprintf("%s_cone_%d.json", stem, number))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}
