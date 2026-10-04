package main

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestTokenOpacityPatchAndReconnect(t *testing.T) {
	for _, opacity := range []float64{0, .5, 1} {
		t.Run(strconv.FormatFloat(opacity, 'f', -1, 64), func(t *testing.T) {
			server := testServer(t, t.TempDir())
			host := httptest.NewServer(server.routes())
			defer host.Close()
			gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Opacity"})
			ws := dial(t, host.URL, gm)
			read(t, ws, "snapshot")
			initialOpacity := 1.0
			if opacity == 1 {
				initialOpacity = .5
			}
			ws.WriteJSON(Command{Type: "create", Client: "opacity", Seq: 1, Token: Token{Name: "Token", Size: 80, Opacity: initialOpacity}})
			token := tokenFrom(t, read(t, ws, "upsert"))
			read(t, ws, "ack")
			ws.WriteJSON(Command{Type: "properties", Client: "opacity", Seq: 2, Token: Token{ID: token.ID}, Properties: Properties{Opacity: &opacity}})
			updated := tokenFrom(t, read(t, ws, "upsert"))
			read(t, ws, "ack")
			if updated.Opacity != opacity {
				t.Fatalf("updated opacity = %v, want %v", updated.Opacity, opacity)
			}
			ws.Close()

			reconnected := dial(t, host.URL, gm)
			snapshot := read(t, reconnected, "snapshot")
			var tokens map[string]Token
			if err := json.Unmarshal(snapshot["tokens"], &tokens); err != nil {
				t.Fatal(err)
			}
			if tokens[token.ID].Opacity != opacity {
				t.Fatalf("reconnected opacity = %v, want %v", tokens[token.ID].Opacity, opacity)
			}
		})
	}
}

func TestTokenOpacityValidation(t *testing.T) {
	server := testServer(t, t.TempDir())
	host := httptest.NewServer(server.routes())
	defer host.Close()
	gm := post(t, host.URL+"/api/sessions", map[string]string{"name": "Opacity validation"})
	ws := dial(t, host.URL, gm)
	read(t, ws, "snapshot")
	ws.WriteJSON(Command{Type: "create", Token: Token{Name: "Token", Size: 80, Opacity: 1}})
	token := tokenFrom(t, read(t, ws, "upsert"))
	invalid := 1.01
	ws.WriteJSON(Command{Type: "properties", Token: Token{ID: token.ID}, Properties: Properties{Opacity: &invalid}})
	read(t, ws, "error")
	if got := firstScene(server.sessions[gm["session"]]).Tokens[token.ID].Opacity; got != 1 {
		t.Fatalf("invalid opacity changed token to %v", got)
	}
}
