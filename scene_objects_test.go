package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func addTestSceneObject(scene *Scene, id, name string) {
	floor := firstFloorID(scene)
	scene.Elements[id] = SceneElement{ID: id, Name: name, FloorID: floor, LayerID: firstLayerID(scene, floor), Visible: true, Opacity: 1, Transform: Transform{X: 100, Y: 100, Width: 50, Height: 50}, Interaction: &ObjectInteraction{}}
	scene.rebuildRuntime()
}

func TestSceneObjectReactionAndProjection(t *testing.T) {
	e := SceneElement{Name: "Stone", Interaction: &ObjectInteraction{Hover: ObjectMessage{Text: "hidden hover"}, Reactions: map[string]ObjectReaction{"inspect": {Any: ObjectMessage{Text: "common"}, Success: ObjectMessage{ImageAssetID: "secret"}}}}}
	for _, row := range []struct {
		success *bool
		want    ObjectMessage
	}{
		{nil, ObjectMessage{Text: "common"}}, {boolPointer(false), ObjectMessage{Text: "common"}}, {boolPointer(true), ObjectMessage{ImageAssetID: "secret"}},
	} {
		if got := objectReaction(e, "inspect", row.success); got != row.want {
			t.Fatalf("reaction: %+v", got)
		}
	}
	if got := objectReaction(e, "other", nil); got.Text != "Stone молчит." {
		t.Fatal(got)
	}
	raw, _ := json.Marshal(projectObjectElement(e))
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "hidden hover") || strings.Contains(string(raw), "common") {
		t.Fatal(string(raw))
	}
	if !activeObject(projectObjectElement(e)) {
		t.Fatal("interaction flag lost")
	}
}

func TestSceneObjectMediaGrantIsolation(t *testing.T) {
	ss := &Session{ID: "a", Assets: map[string]Asset{"private": {ID: "private"}}}
	a, b := &Member{ID: "a"}, &Member{ID: "b"}
	grant := objectMediaPayload(ss, a, "private")["grant"].(string)
	if !validObjectMediaGrant(ss, a, "private", grant) || validObjectMediaGrant(ss, b, "private", grant) || validObjectMediaGrant(ss, a, "other", grant) {
		t.Fatal("invalid grant scope")
	}
}

func addUploadedTestObject(t *testing.T, f diceCommandFixture, credentials map[string]string, sceneID, name string, x, y int) string {
	t.Helper()
	q := url.Values{"session": {credentials["session"]}, "scene": {sceneID}, "kind": {"scene"}, "name": {"object.png"}, "x": {fmt.Sprint(x)}, "y": {fmt.Sprint(y)}}
	req, err := http.NewRequest("POST", f.host.URL+"/api/upload?"+q.Encode(), bytes.NewReader(pipelinePNG(t, 64, 64)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials["key"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var asset Asset
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&asset) != nil {
		t.Fatalf("upload failed: %d", res.StatusCode)
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	ss := f.server.sessions[credentials["session"]]
	scene := ss.Scenes[sceneID]
	for id, e := range scene.Elements {
		if e.AssetID == asset.ID && e.Interaction == nil {
			e.Name = name
			e.Interaction = &ObjectInteraction{}
			scene.Elements[id] = e
			scene.rebuildRuntime()
			return id
		}
	}
	t.Fatal("uploaded element missing")
	return ""
}
