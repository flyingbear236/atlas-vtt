package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type timedBody struct {
	io.ReadCloser
	complete time.Time
}

func (b *timedBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e == io.EOF {
		b.complete = time.Now()
	}
	return n, e
}

// Separate the body read from preparation, with a valid JPEG padded to the
// upload byte limit. Padding is intentional: this is a boundary test, not a
// representative compression ratio. Run sequentially, not during other loads.
func TestProfileTransfer(t *testing.T) {
	out := os.Getenv("ATLAS_PROFILE")
	if out == "" {
		t.Skip("profiling only")
	}
	os.MkdirAll(out, 0755)
	s := testServer(t, t.TempDir())
	base := s.routes()
	var bodySeconds, prepareSeconds float64
	uploaded := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/upload" {
			base.ServeHTTP(w, r)
			return
		}
		b := &timedBody{ReadCloser: r.Body}
		r.Body = b
		at := time.Now()
		base.ServeHTTP(w, r)
		bodySeconds = b.complete.Sub(at).Seconds()
		prepareSeconds = time.Since(b.complete).Seconds()
		close(uploaded)
	}))
	defer ts.Close()
	gm := post(t, ts.URL+"/api/sessions", map[string]string{"name": "Transfer"})
	s.mu.Lock()
	sceneID := firstScene(s.sessions[gm["session"]]).ID
	s.mu.Unlock()
	img := image.NewRGBA(image.Rect(0, 0, 4096, 4096))
	for y := 0; y < 4096; y++ {
		for x := 0; x < 4096; x++ {
			i := y*img.Stride + x*4
			img.Pix[i] = uint8(x*13 + y*7 + (x*y)%251)
			img.Pix[i+1] = uint8(x/16 + y/8)
			img.Pix[i+2] = uint8(y/32 + x/8)
			img.Pix[i+3] = 255
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 75})
	jpegBytes := b.Bytes()
	totalBytes := uploadLimit - 1
	paddingBytes := totalBytes - int64(len(jpegBytes))
	if paddingBytes < 0 {
		t.Fatal("profile JPEG exceeds upload limit")
	}
	body := io.MultiReader(bytes.NewReader(jpegBytes), io.LimitReader(zeroReader{}, paddingBytes))
	req, _ := http.NewRequest("POST", ts.URL+"/api/upload?session="+gm["session"]+"&scene="+sceneID+"&kind=map", body)
	req.ContentLength = totalBytes
	req.Header.Set("Authorization", "Bearer "+gm["key"])
	at := time.Now()
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	<-uploaded
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	result := map[string]any{"bytes": totalBytes, "source": "4096x4096 JPEG + trailing padding", "bodyReadSeconds": bodySeconds, "prepareAndSaveSeconds": prepareSeconds, "totalSeconds": time.Since(at).Seconds()}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	os.WriteFile(filepath.Join(out, "upload-transfer.json"), encoded, 0600)
	t.Log(string(encoded))
}
