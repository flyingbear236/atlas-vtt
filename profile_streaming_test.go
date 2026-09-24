package main

import (
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Fixture generation is streamed too, and is outside the measured server.
type pngChunks struct{ io.Writer }

func (p pngChunks) Write(b []byte) (int, error) { return len(b), pngChunk(p.Writer, "IDAT", b) }
func pngChunk(w io.Writer, kind string, b []byte) error {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(b)))
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, kind); err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	h := crc32.NewIEEE()
	io.WriteString(h, kind)
	h.Write(b)
	binary.BigEndian.PutUint32(size[:], h.Sum32())
	_, err := w.Write(size[:])
	return err
}
func profilePNG(path string, width, height, depth int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	f.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	var header [13]byte
	binary.BigEndian.PutUint32(header[0:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	header[8] = byte(depth)
	header[9] = 6
	if err = pngChunk(f, "IHDR", header[:]); err != nil {
		return err
	}
	z, err := zlib.NewWriterLevel(pngChunks{f}, zlib.BestSpeed)
	if err != nil {
		return err
	}
	step := 4 * depth / 8
	row := make([]byte, 1+width*step)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			values := [4]byte{byte(x / 32), byte(y / 32), byte((x + y) / 64), byte(127 + (x/64%2)*128)}
			for c, v := range values {
				offset := 1 + x*step + c*depth/8
				row[offset] = v
				if depth == 16 {
					row[offset+1] = v
				}
			}
		}
		if _, err = z.Write(row); err != nil {
			return err
		}
	}
	if err = z.Close(); err != nil {
		return err
	}
	return pngChunk(f, "IEND", nil)
}

func TestProfileStreaming(t *testing.T) {
	out := os.Getenv("ATLAS_PROFILE")
	if out == "" {
		t.Skip("profiling only")
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	tool, err := findVips()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProfileHost$", "-test.v")
	cmd.Env = append(os.Environ(), "ATLAS_PROFILE_HOST="+root)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var host struct {
		URL string `json:"url"`
		PID int    `json:"pid"`
	}
	for i := 0; i < 200; i++ {
		b, e := os.ReadFile(filepath.Join(root, "host.json"))
		if e == nil && json.Unmarshal(b, &host) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if host.URL == "" {
		t.Fatal("profile host did not start")
	}
	var rows []map[string]any
	persist := func() {
		b, _ := json.MarshalIndent(map[string]any{"description": "5ms sampled RSS includes separate libvips worker; CPU uses process user+kernel time", "rows": rows}, "", "  ")
		os.WriteFile(filepath.Join(out, "streaming-maps.json"), b, 0600)
	}
	for _, spec := range []struct {
		name        string
		w, h, depth int
		convert     string
	}{
		{"rgba8_10000x10000", 10000, 10000, 8, ""},
		{"rgba16_10000x10000", 10000, 10000, 16, ""},
		{"adam7_rgba8_10000x10000", 10000, 10000, 8, "png"},
		{"progressive_jpeg_10000x10000", 10000, 10000, 8, "jpeg"},
		{"rgba8_32768x3000", 32768, 3000, 8, ""},
	} {
		t.Logf("fixture %s", spec.name)
		input := filepath.Join(root, spec.name+".png")
		if err = profilePNG(input, spec.w, spec.h, spec.depth); err != nil {
			t.Fatal(err)
		}
		if spec.convert != "" {
			dest := filepath.Join(root, spec.name+"-interlaced."+spec.convert)
			operation := "pngsave"
			if spec.convert == "jpeg" {
				operation = "jpegsave"
			}
			if err = runVips(context.Background(), tool, root, operation, input, dest, "--interlace"); err != nil {
				t.Fatal(err)
			}
			os.Remove(input)
			input = dest
		}
		f, e := os.Open(input)
		if e != nil {
			t.Fatal(e)
		}
		st, _ := f.Stat()
		if st.Size() > uploadLimit {
			f.Close()
			t.Fatalf("fixture exceeds upload limit: %d", st.Size())
		}
		t.Logf("upload %s (%d bytes)", spec.name, st.Size())
		req, _ := http.NewRequest("POST", host.URL+"/api/upload?session=load&scene=load-scene&kind=map", f)
		req.Header.Set("Authorization", "Bearer load-key")
		before := readProcessStats(host.PID)
		at := time.Now()
		res, e := http.DefaultClient.Do(req)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		elapsed := time.Since(at).Seconds()
		after := readProcessStats(host.PID)
		var worker map[string]any
		json.Unmarshal(profileJSON(t, host.URL+"/__bench/worker", "GET", nil), &worker)
		row := map[string]any{"name": spec.name, "width": spec.w, "height": spec.h, "uploadBytes": st.Size(), "seconds": elapsed, "status": res.StatusCode, "serverCPUSeconds": after.CPUSeconds - before.CPUSeconds, "worker": worker, "response": string(body)}
		rows = append(rows, row)
		persist()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %s", spec.name, body)
		}
		var a Asset
		if err = json.Unmarshal(body, &a); err != nil {
			t.Fatal(err)
		}
		files, err := filepath.Glob(filepath.Join(root, "data", "assets", a.ID, "*.png"))
		if err != nil {
			t.Fatal(err)
		}
		expected := 0
		for w, h := spec.w, spec.h; ; {
			expected += ((w + 511) / 512) * ((h + 511) / 512)
			if w <= 512 && h <= 512 {
				break
			}
			w = (w + 1) / 2
			h = (h + 1) / 2
		}
		if len(files) != expected {
			t.Fatalf("tiles: got %d want %d", len(files), expected)
		}
		row["tiles"] = len(files)
		var tileBytes int64
		for _, name := range files {
			st, e := os.Stat(name)
			if e != nil {
				t.Fatal(e)
			}
			tileBytes += st.Size()
		}
		row["tileBytes"] = tileBytes
		persist()
		os.Remove(input)
		t.Logf("%s: %.2fs, worker %.1f MiB, server+worker %.1f MiB", spec.name, elapsed, worker["workerPeakWorkingSet"].(float64)/(1<<20), worker["combinedPeakWorkingSet"].(float64)/(1<<20))
	}
	profileJSON(t, host.URL+"/__bench/stop", "POST", nil)
	if err = cmd.Wait(); err != nil {
		t.Fatal(fmt.Errorf("profile host: %w", err))
	}
}
