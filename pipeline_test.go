package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"autopack/utils"
)

type fakeUploader struct {
	requests []utils.UploadRequest
}

func (u *fakeUploader) UploadMany(_ context.Context, requests []utils.UploadRequest) []utils.UploadResult {
	u.requests = append([]utils.UploadRequest(nil), requests...)
	results := make([]utils.UploadResult, len(requests))
	for index, request := range requests {
		results[index] = utils.UploadResult{
			Request: request,
			Asset:   &utils.UploadedAsset{AssetID: fmt.Sprintf("%d", 1000+index)},
		}
	}
	return results
}

func TestRunPipelineBuildsSchemaAndDeduplicatesSharedMeshes(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "pack.zip")
	writeCompletePack(t, zipPath)
	uploader := &fakeUploader{}
	output, err := RunPipeline(context.Background(), zipPath, uploader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output.Values["BlocksVPImage"] != "0" {
		t.Fatalf("BlocksVPImage = %q", output.Values["BlocksVPImage"])
	}
	if rotation, ok := output.Values["GoldAppleRotation"].([]int); !ok || !reflect.DeepEqual(rotation, []int{0, -90, 0}) {
		t.Fatalf("GoldAppleRotation = %#v, want [0 -90 0]", output.Values["GoldAppleRotation"])
	}
	if rotation := output.Values["DiamondRotation"]; !reflect.DeepEqual(rotation, []any{float64(0), float64(0), float64(35)}) {
		t.Fatalf("DiamondRotation = %#v, want default [0 0 35] and never -90", rotation)
	}
	if rotation, ok := output.Values["ShearsRotation"].([]int); !ok || !reflect.DeepEqual(rotation, []int{0, 0, -35}) {
		t.Fatalf("ShearsRotation = %#v, want [0 0 -35]", output.Values["ShearsRotation"])
	}
	for _, forbidden := range []string{"Moon", "Sun", "SkyBack", "SkyBottom", "SkyFront", "SkyLeft", "SkyRight", "SkyTop", "SkyBoxRotationSpeed"} {
		if _, exists := output.Values[forbidden]; exists {
			t.Fatalf("sky field %q should not be exported", forbidden)
		}
	}
	for field, expected := range map[string][]any{
		"SwordScale":       {float64(2), float64(2), float64(2)},
		"PickaxeScale":     {float64(2), float64(2), float64(2)},
		"DiamondScale":     {float64(1.75), float64(1.75), float64(1.75)},
		"DefaultBowScale":  {float64(2.25), float64(2.25), float64(2.25)},
		"JumpPotionScale":  {float64(2), float64(2), float64(2)},
		"MeteorStaffScale": {float64(2), float64(2), float64(2)},
	} {
		if !reflect.DeepEqual(output.Values[field], expected) {
			t.Fatalf("%s = %#v, want default %#v", field, output.Values[field], expected)
		}
	}
	for _, field := range []string{"AmethystSwordTexture", "MeteorStaffMesh", "RegenStaffMesh", "SpeedStaffMesh", "WindStaffMesh"} {
		if output.Values[field] == "" {
			t.Fatalf("default item %s is missing", field)
		}
	}
	if output.Values["SwordMesh"] == "" || output.Values["PickaxeMesh"] == "" || output.Values["AxeMesh"] == "" {
		t.Fatalf("shared mesh fields missing: %#v", output.Values)
	}
	if output.Values["JumpPotionMesh"] != output.Values["SpeedPotionMesh"] {
		t.Fatalf("potion meshes are not shared: %q != %q", output.Values["JumpPotionMesh"], output.Values["SpeedPotionMesh"])
	}
	preview, err := png.Decode(bytes.NewReader(output.PreviewPNG))
	if err != nil {
		t.Fatalf("decode hotbar preview: %v", err)
	}
	if preview.Bounds().Dx() != 440 || preview.Bounds().Dy() != 128 {
		t.Fatalf("hotbar preview size = %v, want 440x128", preview.Bounds().Size())
	}
	if output.Values["GoldSwordTexture"] == "" || output.Values["GoldSwordVPImage"] == "" {
		t.Fatal("iron sword did not populate GoldSword fields")
	}
	if output.Values["PickaxeTexture"] == "88288870749393" || output.Values["AxeTexture"] == "109633191239964" {
		t.Fatal("stone tool textures retained template IDs instead of the uploaded pack textures")
	}
	if output.Values["ClayBlue"] == "" || output.Values["ClayRed"] == "" || output.Values["ClayWhite"] == "" {
		t.Fatal("wool did not populate Clay fields")
	}
	meshes := make(map[string]int)
	for _, request := range uploader.requests {
		if request.AssetType == utils.AssetTypeMesh {
			meshes[request.DisplayName]++
			if filepath.Ext(request.FilePath) != ".mesh" {
				t.Fatalf("mesh upload %q uses %q, want .mesh", request.DisplayName, request.FilePath)
			}
		}
	}
	for _, name := range []string{"Cone sword mesh", "Cone pickaxe mesh", "Cone axe mesh", "Cone potion mesh"} {
		if meshes[name] != 1 {
			t.Fatalf("%q upload count = %d, want one", name, meshes[name])
		}
	}
	notificationJSON, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	notificationContent := fmt.Sprintf("**:exclamation: A Pack has been ported!**\nPack ID: `%s`\n```json\n%s\n```", "0123456789abcdef", notificationJSON)
	if len(notificationContent) > discordMessageLimit {
		t.Fatalf("compressed full template does not fit in Discord: %d characters", len(notificationContent))
	}
	// Every prepared upload lives under the extracted temporary directory and
	// must be gone when RunPipeline returns.
	for _, request := range uploader.requests {
		if _, err := os.Stat(request.FilePath); !os.IsNotExist(err) {
			t.Fatalf("temporary upload still exists: %s (%v)", request.FilePath, err)
		}
	}
}

func TestPipelineMeshesFaceNorthWithHalfTurnYaw(t *testing.T) {
	bindings := pipelineMeshes()
	wantZ := map[string]float64{
		"sword": 180, "pickaxe": 180, "bow_0": 180,
		"gold_apple": 180, "axe": 360, "shears": 180,
	}
	for _, binding := range bindings {
		if expected, exists := wantZ[binding.Name]; exists && binding.Config.RotateZ != expected {
			t.Fatalf("%s RotateZ = %v, want %v", binding.Name, binding.Config.RotateZ, expected)
		}
	}
	var sword, axe, shears utils.Config
	for _, binding := range bindings {
		switch binding.Name {
		case "sword":
			sword = binding.Config
		case "axe":
			axe = binding.Config
		case "shears":
			shears = binding.Config
		}
	}
	if axe.RotateX != sword.RotateX || axe.RotateY != sword.RotateY || axe.RotateZ != sword.RotateZ+180 {
		t.Fatalf("axe rotation = (%v, %v, %v), want sword tilt with 180-degree yaw (%v, %v, %v)",
			axe.RotateX, axe.RotateY, axe.RotateZ, sword.RotateX, sword.RotateY, sword.RotateZ+180)
	}
	if shears != sword {
		t.Fatalf("shears mesh config = %+v, want unmodified standard config %+v", shears, sword)
	}
}

func TestGoldenAppleMeshHasNoCustomRotationOffset(t *testing.T) {
	for _, binding := range pipelineMeshes() {
		if binding.Name != "gold_apple" {
			continue
		}
		if binding.Config.RotateX != 90 || binding.Config.RotateY != 0 || binding.Config.RotateZ != 180 {
			t.Fatalf("gold apple mesh rotation = (%v, %v, %v), want unchanged flat config (90, 0, 180)",
				binding.Config.RotateX, binding.Config.RotateY, binding.Config.RotateZ)
		}
		return
	}
	t.Fatal("gold apple mesh binding is missing")
}

func TestPotionMeshUsesUprightFlatOrientation(t *testing.T) {
	found := 0
	for _, binding := range pipelineMeshes() {
		if binding.Name != "potion" && binding.Name != "fireball" {
			continue
		}
		found++
		if binding.Config.RotateX != 90 || binding.Config.RotateY != 0 || binding.Config.RotateZ != 180 {
			t.Fatalf("%s mesh rotation = (%v, %v, %v), want upright flat config (90, 0, 180)",
				binding.Name, binding.Config.RotateX, binding.Config.RotateY, binding.Config.RotateZ)
		}
	}
	if found != 2 {
		t.Fatalf("found %d upright flat bindings, want potion and fireball", found)
	}
}

func TestWriteExportJSONIsCompressedBufferEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	output := PipelineResult{Values: map[string]any{
		"BlocksVPImage": "0", "SwordMesh": "123", "ClayBlue": "456",
		"GoldAppleRotation": []int{0, -90, 0},
	}}
	if err := writeExportJSON(path, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope compressedJSONEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("output envelope is not strict JSON: %v", err)
	}
	if envelope.Metadata != nil || envelope.Type != "buffer" || envelope.ZBase64 == "" {
		t.Fatalf("unexpected compressed envelope: %#v", envelope)
	}
	decoded, err := decodeCompressedJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded["GoldAppleRotation"], []any{float64(0), float64(-90), float64(0)}) {
		t.Fatalf("GoldAppleRotation = %#v, want [0 -90 0]", decoded["GoldAppleRotation"])
	}
}

func TestRunPipelineReportsMissingTexturesBeforeUpload(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "partial.zip")
	writePackImages(t, zipPath, map[string]image.Image{
		"assets/minecraft/textures/items/diamond_sword.png": testTexture(color.NRGBA{B: 255, A: 255}),
	})
	uploader := &fakeUploader{}
	_, err := RunPipeline(context.Background(), zipPath, uploader, nil)
	if err == nil || !strings.Contains(err.Error(), "missing required textures") {
		t.Fatalf("pipeline error = %v", err)
	}
	if len(uploader.requests) != 0 {
		t.Fatal("pipeline uploaded assets despite missing required textures")
	}
}

func writeCompletePack(t *testing.T, path string) {
	t.Helper()
	files := make(map[string]image.Image)
	items := []string{
		"stone_sword", "diamond_sword", "iron_sword", "wood_sword",
		"stone_pickaxe", "diamond_pickaxe", "iron_pickaxe", "wood_pickaxe",
		"stone_axe", "diamond_axe", "iron_axe", "wood_axe",
		"bow_standby", "bow_pulling_0", "bow_pulling_1", "bow_pulling_2",
		"apple_golden", "iron_ingot", "diamond", "emerald", "ender_pearl", "shears", "fireball",
	}
	for index, name := range items {
		files["Wrapper/assets/minecraft/textures/items/"+name+".png"] = testTexture(color.NRGBA{
			R: uint8(20 + index), G: 100, B: 180, A: 255,
		})
	}
	files["Wrapper/assets/minecraft/textures/items/potion_overlay.png"] = testTexture(color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	files["Wrapper/assets/minecraft/textures/items/potion_bottle_drinkable.png"] = image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for _, name := range []string{"blue", "cyan", "green", "gray", "orange", "purple", "red", "white", "yellow"} {
		files["Wrapper/assets/minecraft/textures/blocks/wool_colored_"+name+".png"] = testTexture(color.NRGBA{R: 160, G: 80, B: 40, A: 255})
	}
	writePackImages(t, path, files)
}

func writePackImages(t *testing.T, path string, files map[string]image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, img := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(entry, img); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func testTexture(pixel color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 3; y < 13; y++ {
		for x := 5; x < 11; x++ {
			img.SetNRGBA(x, y, pixel)
		}
	}
	return img
}
