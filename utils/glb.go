package utils

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const (
	// Version identifies the AutoPack library in generated GLB metadata.
	Version = "1.3.0"

	glbMagic       = 0x46546c67
	glbVersion     = 2
	chunkJSON      = 0x4e4f534a
	chunkBIN       = 0x004e4942
	componentU16   = 5123
	componentU32   = 5125
	componentFloat = 5126
	targetArray    = 34962
	targetElements = 34963
)

type bufferView struct {
	Buffer     int `json:"buffer"`
	ByteOffset int `json:"byteOffset,omitempty"`
	ByteLength int `json:"byteLength"`
	Target     int `json:"target,omitempty"`
}

type accessor struct {
	BufferView    int       `json:"bufferView"`
	ComponentType int       `json:"componentType"`
	Count         int       `json:"count"`
	Type          string    `json:"type"`
	Min           []float32 `json:"min,omitempty"`
	Max           []float32 `json:"max,omitempty"`
}

// EncodeGLB writes glTF 2.0 directly. It deliberately uses no Blender or CGo
// dependency, and embeds the source PNG so the output remains a single file.
func EncodeGLB(mesh Mesh, pngBytes []byte, imageName string) ([]byte, error) {
	vertexCount := len(mesh.Positions) / 3
	if vertexCount == 0 || len(mesh.Positions)%3 != 0 {
		return nil, errors.New("mesh has no valid positions")
	}
	if len(mesh.Normals) != len(mesh.Positions) || len(mesh.TexCoords) != vertexCount*2 {
		return nil, errors.New("mesh attribute counts do not match")
	}
	if len(mesh.Indices) == 0 || len(mesh.Indices)%3 != 0 {
		return nil, errors.New("mesh has no valid triangles")
	}
	if len(pngBytes) < 8 || !bytes.Equal(pngBytes[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return nil, errors.New("texture is not a PNG")
	}

	bin := make([]byte, 0, len(mesh.Positions)*4+len(mesh.Normals)*4+len(mesh.TexCoords)*4+len(mesh.Indices)*4+len(pngBytes)+32)
	views := make([]bufferView, 0, 5)
	appendView := func(raw []byte, target int) int {
		for len(bin)%4 != 0 {
			bin = append(bin, 0)
		}
		offset := len(bin)
		bin = append(bin, raw...)
		views = append(views, bufferView{Buffer: 0, ByteOffset: offset, ByteLength: len(raw), Target: target})
		return len(views) - 1
	}

	positionView := appendView(floatsToBytes(mesh.Positions), targetArray)
	normalView := appendView(floatsToBytes(mesh.Normals), targetArray)
	uvView := appendView(floatsToBytes(mesh.TexCoords), targetArray)
	indexComponent := componentU32
	var indexBytes []byte
	if vertexCount <= math.MaxUint16 {
		indexComponent = componentU16
		indexBytes = make([]byte, len(mesh.Indices)*2)
		for i, index := range mesh.Indices {
			if index > math.MaxUint16 {
				return nil, fmt.Errorf("index %d exceeds uint16 vertex range", index)
			}
			binary.LittleEndian.PutUint16(indexBytes[i*2:], uint16(index))
		}
	} else {
		indexBytes = make([]byte, len(mesh.Indices)*4)
		for i, index := range mesh.Indices {
			binary.LittleEndian.PutUint32(indexBytes[i*4:], index)
		}
	}
	indexView := appendView(indexBytes, targetElements)
	imageView := appendView(pngBytes, 0)
	for len(bin)%4 != 0 {
		bin = append(bin, 0)
	}

	minPos := []float32{mesh.Positions[0], mesh.Positions[1], mesh.Positions[2]}
	maxPos := append([]float32(nil), minPos...)
	for i := 3; i < len(mesh.Positions); i += 3 {
		for axis := 0; axis < 3; axis++ {
			value := mesh.Positions[i+axis]
			if value < minPos[axis] {
				minPos[axis] = value
			}
			if value > maxPos[axis] {
				maxPos[axis] = value
			}
		}
	}
	accessors := []accessor{
		{BufferView: positionView, ComponentType: componentFloat, Count: vertexCount, Type: "VEC3", Min: minPos, Max: maxPos},
		{BufferView: normalView, ComponentType: componentFloat, Count: vertexCount, Type: "VEC3"},
		{BufferView: uvView, ComponentType: componentFloat, Count: vertexCount, Type: "VEC2"},
		{BufferView: indexView, ComponentType: indexComponent, Count: len(mesh.Indices), Type: "SCALAR"},
	}

	alphaMode := "OPAQUE"
	if mesh.AlphaBlend {
		alphaMode = "BLEND"
	}
	doc := map[string]any{
		"asset":  map[string]any{"version": "2.0", "generator": "AutoPack Go " + Version},
		"scene":  0,
		"scenes": []any{map[string]any{"name": "Scene", "nodes": []int{0}}},
		"nodes":  []any{map[string]any{"name": "PixelArtObject", "mesh": 0}},
		"meshes": []any{map[string]any{
			"name": "PixelArtMesh",
			"primitives": []any{map[string]any{
				"attributes": map[string]int{"POSITION": 0, "NORMAL": 1, "TEXCOORD_0": 2},
				"indices":    3, "material": 0, "mode": 4,
			}},
		}},
		"materials": []any{map[string]any{
			"name":        "PixelArtMaterial",
			"doubleSided": true,
			"alphaMode":   alphaMode,
			"pbrMetallicRoughness": map[string]any{
				"baseColorTexture": map[string]int{"index": 0},
				"metallicFactor":   0,
				"roughnessFactor":  1,
			},
		}},
		"textures":    []any{map[string]any{"name": "PixelArtTexture", "sampler": 0, "source": 0}},
		"samplers":    []any{map[string]any{"magFilter": 9728, "minFilter": 9728}},
		"images":      []any{map[string]any{"name": imageName, "bufferView": imageView, "mimeType": "image/png"}},
		"accessors":   accessors,
		"bufferViews": views,
		"buffers":     []any{map[string]any{"byteLength": len(bin)}},
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	for len(jsonBytes)%4 != 0 {
		jsonBytes = append(jsonBytes, ' ')
	}
	total := 12 + 8 + len(jsonBytes) + 8 + len(bin)
	out := bytes.NewBuffer(make([]byte, 0, total))
	_ = binary.Write(out, binary.LittleEndian, uint32(glbMagic))
	_ = binary.Write(out, binary.LittleEndian, uint32(glbVersion))
	_ = binary.Write(out, binary.LittleEndian, uint32(total))
	_ = binary.Write(out, binary.LittleEndian, uint32(len(jsonBytes)))
	_ = binary.Write(out, binary.LittleEndian, uint32(chunkJSON))
	_, _ = out.Write(jsonBytes)
	_ = binary.Write(out, binary.LittleEndian, uint32(len(bin)))
	_ = binary.Write(out, binary.LittleEndian, uint32(chunkBIN))
	_, _ = out.Write(bin)
	return out.Bytes(), nil
}

func floatsToBytes(values []float32) []byte {
	out := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(value))
	}
	return out
}
