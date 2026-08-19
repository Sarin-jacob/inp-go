package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- DATA STRUCTURES ---

type Node struct {
	ID      int
	X, Y, Z float32
}

type Element struct {
	Etype string
	Nodes []int
}

type FaceKey [4]int

// --- MATH HELPERS (For STL Normals) ---

func calculateNormal(p1, p2, p3 Node) (float32, float32, float32) {
	// Vector U = p2 - p1
	ux := p2.X - p1.X
	uy := p2.Y - p1.Y
	uz := p2.Z - p1.Z
	
	// Vector V = p3 - p1
	vx := p3.X - p1.X
	vy := p3.Y - p1.Y
	vz := p3.Z - p1.Z

	// Cross Product (U x V)
	nx := (uy * vz) - (uz * vy)
	ny := (uz * vx) - (ux * vz)
	nz := (ux * vy) - (uy * vx)

	// Normalize the vector
	length := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
	if length == 0 {
		return 0, 0, 0
	}
	return nx / length, ny / length, nz / length
}

// --- CORE PARSING ---

func parseINP(path string) (map[int]Node, []Element, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	nodes := make(map[int]Node)
	var elements []Element
	scanner := bufio.NewScanner(file)
	state, currentEtype := "", ""
	var currentNodes []int

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "**") {
			continue
		}

		upperLine := strings.ToUpper(line)
		if strings.HasPrefix(upperLine, "*") {
			if currentNodes != nil && currentEtype != "" {
				elements = append(elements, Element{Etype: currentEtype, Nodes: currentNodes})
				currentNodes = nil
			}
			if strings.HasPrefix(upperLine, "*NODE") {
				state = "NODE"
			} else if strings.HasPrefix(upperLine, "*ELEMENT") {
				state = "ELEMENT"
				if idx := strings.Index(upperLine, "TYPE="); idx != -1 {
					parts := strings.SplitN(upperLine[idx+5:], ",", 2)
					currentEtype = strings.TrimSpace(parts[0])
				}
			} else {
				state = ""
			}
			continue
		}

		parts := strings.Split(line, ",")
		if state == "NODE" && len(parts) >= 3 {
			id, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			x, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 32)
			y, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 32)
			z := 0.0
			if len(parts) > 3 && strings.TrimSpace(parts[3]) != "" {
				z, _ = strconv.ParseFloat(strings.TrimSpace(parts[3]), 32)
			}
			nodes[id] = Node{ID: id, X: float32(x), Y: float32(y), Z: float32(z)}
		} else if state == "ELEMENT" {
			for i, p := range parts {
				val := strings.TrimSpace(p)
				if val == "" { continue }
				if nodeID, err := strconv.Atoi(val); err == nil {
					if i == 0 && len(currentNodes) == 0 { continue }
					currentNodes = append(currentNodes, nodeID)
				}
			}
			if !strings.HasSuffix(line, ",") {
				elements = append(elements, Element{Etype: currentEtype, Nodes: currentNodes})
				currentNodes = nil
			}
		}
	}
	return nodes, elements, nil
}

func extractFaces(etype string, enodes []int) [][]int {
	upType := strings.ToUpper(etype)
	if strings.Contains(upType, "3D8") || strings.Contains(upType, "3D20") {
		if len(enodes) >= 8 {
			n := enodes
			return [][]int{{n[0], n[1], n[2], n[3]}, {n[4], n[7], n[6], n[5]}, {n[0], n[4], n[5], n[1]}, {n[1], n[5], n[6], n[2]}, {n[2], n[6], n[7], n[3]}, {n[3], n[7], n[4], n[0]}}
		}
	} else if strings.Contains(upType, "3D4") || strings.Contains(upType, "3D10") {
		if len(enodes) >= 4 {
			n := enodes
			return [][]int{{n[0], n[1], n[2]}, {n[0], n[3], n[1]}, {n[1], n[3], n[2]}, {n[2], n[3], n[0]}}
		}
	} else if strings.Contains(upType, "3D6") || strings.Contains(upType, "3D15") {
		if len(enodes) >= 6 {
			n := enodes
			return [][]int{{n[0], n[1], n[2]}, {n[3], n[5], n[4]}, {n[0], n[3], n[4], n[1]}, {n[1], n[4], n[5], n[2]}, {n[2], n[5], n[3], n[0]}}
		}
	} else if strings.Contains(upType, "S4") || strings.Contains(upType, "PE4") || strings.Contains(upType, "PS4") {
		if len(enodes) >= 4 { return [][]int{{enodes[0], enodes[1], enodes[2], enodes[3]}} }
	} else if strings.Contains(upType, "S3") || strings.Contains(upType, "PE3") || strings.Contains(upType, "PS3") {
		if len(enodes) >= 3 { return [][]int{{enodes[0], enodes[1], enodes[2]}} }
	}
	return nil
}

func getFaceKey(face []int) FaceKey {
	sorted := make([]int, len(face))
	copy(sorted, face)
	sort.Ints(sorted)
	var key FaceKey
	copy(key[:], sorted)
	return key
}

func processChunk(elements []Element, resultChan chan<- map[FaceKey][]int, wg *sync.WaitGroup) {
	defer wg.Done()
	counts := make(map[FaceKey]int)
	faceMap := make(map[FaceKey][]int)

	for _, el := range elements {
		faces := extractFaces(el.Etype, el.Nodes)
		for _, f := range faces {
			key := getFaceKey(f)
			counts[key]++
			faceMap[key] = f
		}
	}

	localShell := make(map[FaceKey][]int)
	for k, count := range counts {
		if count == 1 { localShell[k] = faceMap[k] }
	}
	resultChan <- localShell
}

// --- EXPORTERS ---

func exportOBJ(path string, nodes map[int]Node, outerFaces [][]int) {
	file, _ := os.Create(path)
	defer file.Close()
	writer := bufio.NewWriter(file)
	writer.WriteString("# Converted by INP Parser\n")

	usedNodes := make(map[int]bool)
	for _, f := range outerFaces {
		for _, nid := range f { usedNodes[nid] = true }
	}

	var orderedIDs []int
	for nid := range usedNodes { orderedIDs = append(orderedIDs, nid) }
	sort.Ints(orderedIDs)

	nodeToObjIdx := make(map[int]int)
	for i, nid := range orderedIDs {
		nodeToObjIdx[nid] = i + 1
		writer.WriteString(fmt.Sprintf("v %.6f %.6f %.6f\n", nodes[nid].X, nodes[nid].Y, nodes[nid].Z))
	}
	for _, f := range outerFaces {
		writer.WriteString("f")
		for _, nid := range f { writer.WriteString(fmt.Sprintf(" %d", nodeToObjIdx[nid])) }
		writer.WriteString("\n")
	}
	writer.Flush()
}

func exportSTL(path string, nodes map[int]Node, outerFaces [][]int) {
	file, _ := os.Create(path)
	defer file.Close()

	// 1. Write 80-byte header (Required by STL standard)
	header := make([]byte, 80)
	copy(header, []byte("INP Parser - Binary STL Export"))
	file.Write(header)

	// 2. Calculate and write total number of triangles
	var numTriangles uint32 = 0
	for _, f := range outerFaces {
		if len(f) == 4 {
			numTriangles += 2
		} else if len(f) == 3 {
			numTriangles += 1
		}
	}
	binary.Write(file, binary.LittleEndian, numTriangles)

	// 3. Write Triangles
	var attr uint16 = 0
	for _, f := range outerFaces {
		if len(f) == 4 {
			// Split Quad into Two Triangles
			n1, n2, n3, n4 := nodes[f[0]], nodes[f[1]], nodes[f[2]], nodes[f[3]]
			
			// Triangle 1 (n1, n2, n3)
			nx, ny, nz := calculateNormal(n1, n2, n3)
			binary.Write(file, binary.LittleEndian, []float32{nx, ny, nz, n1.X, n1.Y, n1.Z, n2.X, n2.Y, n2.Z, n3.X, n3.Y, n3.Z})
			binary.Write(file, binary.LittleEndian, attr)
			
			// Triangle 2 (n1, n3, n4)
			nx, ny, nz = calculateNormal(n1, n3, n4)
			binary.Write(file, binary.LittleEndian, []float32{nx, ny, nz, n1.X, n1.Y, n1.Z, n3.X, n3.Y, n3.Z, n4.X, n4.Y, n4.Z})
			binary.Write(file, binary.LittleEndian, attr)
			
		} else if len(f) == 3 {
			// Single Triangle
			n1, n2, n3 := nodes[f[0]], nodes[f[1]], nodes[f[2]]
			nx, ny, nz := calculateNormal(n1, n2, n3)
			binary.Write(file, binary.LittleEndian, []float32{nx, ny, nz, n1.X, n1.Y, n1.Z, n2.X, n2.Y, n2.Z, n3.X, n3.Y, n3.Z})
			binary.Write(file, binary.LittleEndian, attr)
		}
	}
}

func exportGLB(path string, nodes map[int]Node, outerFaces [][]int) {
	file, _ := os.Create(path)
	defer file.Close()

	usedNodes := make(map[int]bool)
	for _, f := range outerFaces {
		for _, nid := range f { usedNodes[nid] = true }
	}

	var orderedIDs []int
	for nid := range usedNodes { orderedIDs = append(orderedIDs, nid) }
	sort.Ints(orderedIDs)

	nodeToIdx := make(map[int]uint32)
	var vertices, colors []float32

	for i, nid := range orderedIDs {
		nodeToIdx[nid] = uint32(i)
		n := nodes[nid]
		vertices = append(vertices, n.X, n.Y, n.Z)

		var val float32 = n.Z
		if val < 0 { val = -val }
		for val > 1.0 { val /= 10.0 }
		colors = append(colors, val, 0.2, 1.0-val, 1.0)
	}

	var indices []uint32
	for _, f := range outerFaces {
		if len(f) == 4 {
			i0, i1, i2, i3 := nodeToIdx[f[0]], nodeToIdx[f[1]], nodeToIdx[f[2]], nodeToIdx[f[3]]
			indices = append(indices, i0, i1, i2, i0, i2, i3)
		} else if len(f) == 3 {
			indices = append(indices, nodeToIdx[f[0]], nodeToIdx[f[1]], nodeToIdx[f[2]])
		}
	}

	binBuffer := new(bytes.Buffer)
	binary.Write(binBuffer, binary.LittleEndian, vertices)
	binary.Write(binBuffer, binary.LittleEndian, colors)
	for binBuffer.Len()%4 != 0 { binBuffer.WriteByte(0) }
	
	indicesOffset := binBuffer.Len()
	binary.Write(binBuffer, binary.LittleEndian, indices)
	
	jsonStr := fmt.Sprintf(`{"asset": {"version": "2.0"},"scene": 0,"scenes": [{"nodes": [0]}],"nodes": [{"mesh": 0}],"meshes": [{"primitives": [{"attributes": {"POSITION": 0, "COLOR_0": 1}, "indices": 2, "material": 0}]}],"materials": [{"pbrMetallicRoughness": {"baseColorFactor": [1, 1, 1, 1], "metallicFactor": 0.1, "roughnessFactor": 0.5}, "doubleSided": true}],"buffers": [{"byteLength": %d}],"bufferViews": [{"buffer": 0, "byteOffset": 0, "byteLength": %d, "target": 34962},{"buffer": 0, "byteOffset": %d, "byteLength": %d, "target": 34962},{"buffer": 0, "byteOffset": %d, "byteLength": %d, "target": 34963}],"accessors": [{"bufferView": 0, "byteOffset": 0, "componentType": 5126, "count": %d, "type": "VEC3"},{"bufferView": 1, "byteOffset": 0, "componentType": 5126, "count": %d, "type": "VEC4"},{"bufferView": 2, "byteOffset": 0, "componentType": 5125, "count": %d, "type": "SCALAR"}]}`, 
		binBuffer.Len(), len(vertices)*4, len(vertices)*4, len(colors)*4, indicesOffset, len(indices)*4, len(vertices)/3, len(colors)/4, len(indices))

	jsonBytes := []byte(jsonStr)
	for len(jsonBytes)%4 != 0 { jsonBytes = append(jsonBytes, ' ') }

	binary.Write(file, binary.LittleEndian, []byte("glTF"))
	binary.Write(file, binary.LittleEndian, uint32(2))
	binary.Write(file, binary.LittleEndian, uint32(12+8+len(jsonBytes)+8+binBuffer.Len()))
	binary.Write(file, binary.LittleEndian, uint32(len(jsonBytes)))
	binary.Write(file, binary.LittleEndian, []byte("JSON"))
	file.Write(jsonBytes)
	binary.Write(file, binary.LittleEndian, uint32(binBuffer.Len()))
	binary.Write(file, binary.LittleEndian, []byte("BIN\x00"))
	file.Write(binBuffer.Bytes())
}

// --- MAIN CLI ---

func main() {
	formatPtr := flag.String("format", "glb", "Comma-separated list (e.g. stl,glb,obj) or 'all'")
	flag.Parse()

	if len(flag.Args()) < 1 {
		fmt.Println("Usage: inpparser -format=stl,glb <file.inp>")
		return
	}
	inpPath := flag.Args()[0]
	baseName := strings.TrimSuffix(filepath.Base(inpPath), filepath.Ext(inpPath))
	formats := strings.ToLower(*formatPtr)

	startTime := time.Now()
	fmt.Printf("Parsing %s...\n", inpPath)
	
	nodes, elements, err := parseINP(inpPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Parsed %d nodes, %d elements in %v\n", len(nodes), len(elements), time.Since(startTime))

	extractStart := time.Now()
	numWorkers := runtime.NumCPU()
	chunkSize := len(elements) / numWorkers
	if chunkSize == 0 { chunkSize = 1 }

	var wg sync.WaitGroup
	resultChan := make(chan map[FaceKey][]int, numWorkers)

	for i := 0; i < len(elements); i += chunkSize {
		end := i + chunkSize
		if end > len(elements) { end = len(elements) }
		wg.Add(1)
		go processChunk(elements[i:end], resultChan, &wg)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	globalCounts := make(map[FaceKey]int)
	globalFaces := make(map[FaceKey][]int)

	for localShell := range resultChan {
		for k, f := range localShell {
			globalCounts[k]++
			if _, exists := globalFaces[k]; !exists {
				globalFaces[k] = f
			}
		}
	}

	var outerFaces [][]int
	for k, count := range globalCounts {
		if count == 1 {
			outerFaces = append(outerFaces, globalFaces[k])
		}
	}
	fmt.Printf("Extraction complete in %v. Found %d outer shell faces.\n", time.Since(extractStart), len(outerFaces))

	// Exports
	if strings.Contains(formats, "obj") || formats == "all" {
		exportOBJ(baseName+".obj", nodes, outerFaces)
		fmt.Println("-> Generated", baseName+".obj")
	}
	if strings.Contains(formats, "stl") || formats == "all" {
		exportSTL(baseName+".stl", nodes, outerFaces)
		fmt.Println("-> Generated", baseName+".stl")
	}
	if strings.Contains(formats, "glb") || formats == "all" {
		exportGLB(baseName+".glb", nodes, outerFaces)
		fmt.Println("-> Generated", baseName+".glb")
	}

	fmt.Printf("Total execution time: %v\n", time.Since(startTime))
}