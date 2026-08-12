package utils

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestEncodeGLBIsSelfContainedAndValidlyStructured(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 230, G: 20, B: 40, A: 255})
	mesh, _, err := BuildGreedyMesh(img, Config{
		PlaneSize: 2, Thickness: 0.07, Center: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var texture bytes.Buffer
	if err := png.Encode(&texture, img); err != nil {
		t.Fatal(err)
	}
	glb, err := EncodeGLB(mesh, texture.Bytes(), "one.png")
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(glb[0:4]); got != glbMagic {
		t.Fatalf("magic = %#x", got)
	}
	if got := binary.LittleEndian.Uint32(glb[4:8]); got != glbVersion {
		t.Fatalf("version = %d", got)
	}
	if got := int(binary.LittleEndian.Uint32(glb[8:12])); got != len(glb) {
		t.Fatalf("declared length %d != %d", got, len(glb))
	}
	jsonLen := int(binary.LittleEndian.Uint32(glb[12:16]))
	if got := binary.LittleEndian.Uint32(glb[16:20]); got != chunkJSON {
		t.Fatalf("first chunk = %#x", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(glb[20:20+jsonLen], &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc["images"].([]any)) != 1 {
		t.Fatal("embedded image missing")
	}
	if len(doc["buffers"].([]any)) != 1 {
		t.Fatal("single embedded buffer missing")
	}
	material := doc["materials"].([]any)[0].(map[string]any)
	if material["alphaMode"] != "OPAQUE" {
		t.Fatalf("opaque source alpha mode = %v, want OPAQUE", material["alphaMode"])
	}
	binHeader := 20 + jsonLen
	binLen := int(binary.LittleEndian.Uint32(glb[binHeader : binHeader+4]))
	if got := binary.LittleEndian.Uint32(glb[binHeader+4 : binHeader+8]); got != chunkBIN {
		t.Fatalf("second chunk = %#x", got)
	}
	if binHeader+8+binLen != len(glb) {
		t.Fatal("BIN chunk does not fill GLB")
	}

	mesh.AlphaBlend = true
	glb, err = EncodeGLB(mesh, texture.Bytes(), "one.png")
	if err != nil {
		t.Fatal(err)
	}
	jsonLen = int(binary.LittleEndian.Uint32(glb[12:16]))
	if err := json.Unmarshal(glb[20:20+jsonLen], &doc); err != nil {
		t.Fatal(err)
	}
	material = doc["materials"].([]any)[0].(map[string]any)
	if material["alphaMode"] != "BLEND" {
		t.Fatalf("partial-alpha source alpha mode = %v, want BLEND", material["alphaMode"])
	}
}
