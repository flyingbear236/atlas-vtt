package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func writeScreenshot(path, data string) error {
	b, e := base64.StdEncoding.DecodeString(data)
	if e != nil {
		return e
	}
	return os.WriteFile(path, b, 0600)
}

// Optional real-browser smoke test. ATLAS_CHROME points to Chrome or Edge.
func TestBrowser(t *testing.T) {
	chrome := os.Getenv("ATLAS_CHROME")
	if chrome == "" {
		t.Skip("set ATLAS_CHROME for real browser checks")
	}
	s := testServer(t, t.TempDir())
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	profile := t.TempDir()
	cmd := exec.Command(chrome, "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-default-apps", "--disable-background-networking", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var port string
	for i := 0; i < 100; i++ {
		if b, e := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); e == nil {
			port = strings.Split(string(b), "\n")[0]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		t.Fatal("Chrome did not start")
	}
	res, e := http.Get("http://127.0.0.1:" + port + "/json/version")
	if e != nil {
		t.Fatal(e)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	json.NewDecoder(res.Body).Decode(&version)
	res.Body.Close()
	if version.WebSocketDebuggerURL == "" {
		t.Fatal("Chrome browser target unavailable")
	}
	ws, _, e := websocket.DefaultDialer.Dial(version.WebSocketDebuggerURL, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	seq := 0
	browserCall := func(method string, params any) json.RawMessage {
		seq++
		requestID := seq
		if err := ws.WriteJSON(map[string]any{"id": requestID, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			var message struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  any             `json:"error"`
			}
			if err := ws.ReadJSON(&message); err != nil {
				t.Fatal(err)
			}
			if message.ID != requestID {
				continue
			}
			if message.Error != nil {
				t.Fatalf("DevTools %s: %v", method, message.Error)
			}
			return message.Result
		}
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	json.Unmarshal(browserCall("Target.createTarget", map[string]any{"url": ts.URL}), &created)
	time.Sleep(2 * time.Second)
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(browserCall("Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}), &attached)
	if attached.SessionID == "" {
		t.Fatal("Chrome page session unavailable")
	}
	var browserErrors []string
	call := func(method string, params any) json.RawMessage {
		for attempt := 0; ; attempt++ {
			seq++
			requestID := seq
			ws.WriteJSON(map[string]any{"id": requestID, "method": method, "params": params, "sessionId": attached.SessionID})
			ws.SetReadDeadline(time.Now().Add(60 * time.Second))
			for {
				var msg struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
					ID     int             `json:"id"`
					Result json.RawMessage `json:"result"`
					Error  any             `json:"error"`
				}
				if e := ws.ReadJSON(&msg); e != nil {
					t.Fatal(e)
				}
				if msg.Method == "Runtime.exceptionThrown" {
					browserErrors = append(browserErrors, string(msg.Params))
				}
				if msg.ID != requestID {
					continue
				}
				if msg.Error == nil {
					return msg.Result
				}
				errorJSON, _ := json.Marshal(msg.Error)
				if method == "Runtime.evaluate" && attempt < 10 && strings.Contains(string(errorJSON), "navigated or closed") {
					time.Sleep(200 * time.Millisecond)
					break
				}
				t.Fatalf("DevTools %s: %v", method, msg.Error)
			}
		}
	}
	call("Page.enable", map[string]any{})
	call("Runtime.enable", map[string]any{})
	call("Inspector.enable", map[string]any{})
	call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false})
	time.Sleep(time.Second)
	eval := func(js string) string {
		r := call("Runtime.evaluate", map[string]any{"expression": js, "awaitPromise": true, "returnByValue": true})
		var out struct {
			Result struct {
				Value json.RawMessage `json:"value"`
			} `json:"result"`
			Exception any `json:"exceptionDetails"`
		}
		json.Unmarshal(r, &out)
		if out.Exception != nil {
			t.Fatalf("JavaScript exception: %s; browser errors: %v", r, browserErrors)
		}
		return string(out.Result.Value)
	}
	eval(`(()=>{const original=WebSocket.prototype.send;window.sentCommands=[];WebSocket.prototype.send=function(data){try{window.sentCommands.push(JSON.parse(data))}catch{}return original.call(this,data)};return true})()`)
	result := eval(`(async()=>{
 const sleep=ms=>new Promise(r=>setTimeout(r,ms));
 const wait=async f=>{for(let i=0;i<300;i++){if(f())return;await sleep(100)}throw Error('UI timeout')};
 document.getElementById('sessionName').value='Browser expedition';document.getElementById('startButton').click();
 await wait(()=>document.getElementById('connection').textContent.includes('В сети'));
 if(document.getElementById('campaignHome').hidden)throw Error('campaign home not shown');
 if(!document.getElementById('sceneTabs').hidden)throw Error('scene tabs shown on campaign home');
 if(!document.getElementById('leaveCampaign'))throw Error('campaign exit missing');
 if(document.getElementById('invite').nextElementSibling!==document.getElementById('toScenes'))throw Error('to scenes is not after invite');
 if(performance.getEntriesByType('resource').some(r=>r.name.includes('/api/asset/')))throw Error('campaign home loaded scene assets');
 window.prompt=()=> 'Hidden Browser';document.getElementById('homeSceneAdd').click();await wait(()=>document.querySelectorAll('.scene-card').length===2);
 const hidden=[...document.querySelectorAll('.scene-card')].find(card=>card.textContent.includes('Hidden Browser'));if(!hidden||!hidden.classList.contains('gm-only'))throw Error('hidden scene card missing');
 hidden.querySelector('.primary').click();await wait(()=>!document.getElementById('workspace').hidden);
 if(document.getElementById('sceneTabs').hidden)throw Error('scene tabs hidden inside scene');
 if(document.getElementById('sidebarMap').hidden||!document.getElementById('sidebarObjects').hidden||!document.getElementById('sidebarMembers').hidden)throw Error('map sidebar is not default');
 document.getElementById('objectsTab').click();if(document.getElementById('sidebarObjects').hidden||!document.getElementById('sidebarMap').hidden)throw Error('objects sidebar did not open');
 document.getElementById('membersTab').click();if(document.getElementById('sidebarMembers').hidden||!document.getElementById('sidebarObjects').hidden)throw Error('members sidebar did not open');
 document.getElementById('mapTab').click();document.getElementById('sidebarToggle').click();if(!document.getElementById('sidebarFrame').classList.contains('collapsed'))throw Error('sidebar did not collapse');
 document.getElementById('objectsTab').click();if(document.getElementById('sidebarFrame').classList.contains('collapsed')||document.getElementById('sidebarObjects').hidden)throw Error('scene tab did not restore sidebar');
 if(document.getElementById('demo')||document.getElementById('sceneSelect')||document.getElementById('sceneDelete'))throw Error('removed scene controls still exist');
 if(document.querySelectorAll('.token-row').length)throw Error('new scene contains old tokens');
 document.getElementById('toScenes').click();await wait(()=>!document.getElementById('campaignHome').hidden);
 let published=[...document.querySelectorAll('.scene-card')].find(card=>card.classList.contains('published'));published.querySelector('.primary').click();await wait(()=>!document.getElementById('workspace').hidden);
 if(document.querySelectorAll('.token-row').length)throw Error('tokens mixed while switching scenes');
 const creds=JSON.parse(localStorage.getItem('atlas-sessions'))[0],sceneId=published.dataset.sceneId,c=document.createElement('canvas');c.width=1024;c.height=768;const g=c.getContext('2d');g.fillStyle='#344740';g.fillRect(0,0,c.width,c.height);for(let i=0;i<64;i++){g.fillStyle='hsl('+(i*19%360)+' 28% '+(32+i%18)+'%)';g.fillRect((i%8)*128,Math.floor(i/8)*96,128,96)}g.strokeStyle='#d0c6a4';g.lineWidth=12;g.strokeRect(120,100,780,560);const blob=await new Promise(resolve=>c.toBlob(resolve,'image/png'));
 const uploadResponse=await fetch('/api/upload?session='+encodeURIComponent(creds.session)+'&scene='+encodeURIComponent(sceneId)+'&kind=map',{method:'POST',headers:{Authorization:'Bearer '+creds.key},body:blob});if(!uploadResponse.ok)throw Error('map upload failed: '+uploadResponse.status);
 await wait(()=>document.getElementById('emptyMap').hidden);
 document.getElementById('add').click();document.getElementById('add').click();await wait(()=>document.querySelectorAll('.token-row').length===2);
 document.querySelector('.token-row').click();document.getElementById('tokenName').value='Browser hero';document.getElementById('tokenName').dispatchEvent(new Event('input',{bubbles:true}));document.getElementById('properties').requestSubmit();await wait(()=>document.querySelector('.token-row').textContent.includes('Browser hero'));
 document.getElementById('fit').click();await sleep(2500);document.getElementById('reconnect').click();await sleep(2000);await wait(()=>document.getElementById('connection').textContent.includes('В сети'));await wait(()=>!document.getElementById('workspace').hidden);await wait(()=>document.getElementById('diagnostics').textContent.includes('Загрузка 0'));
 const requests=performance.getEntriesByType('resource').filter(r=>r.name.includes('/api/asset/')).length;document.getElementById('toScenes').click();await wait(()=>!document.getElementById('campaignHome').hidden);await sleep(500);if(performance.getEntriesByType('resource').filter(r=>r.name.includes('/api/asset/')).length!==requests)throw Error('campaign home continued scene loading');
 published=[...document.querySelectorAll('.scene-card')].find(card=>card.classList.contains('published'));published.querySelector('.primary').click();await wait(()=>!document.getElementById('workspace').hidden);await wait(()=>[...document.querySelectorAll('.token-row')].some(row=>row.textContent.includes('Browser hero')));
 return {tokens:document.querySelectorAll('.token-row').length,mapLoaded:document.getElementById('emptyMap').hidden,diagnostics:document.getElementById('diagnostics').textContent};
})()`)
	t.Log(result)
	if eval(`sentCommands.filter(c=>c.type==='properties').every(c=>!('x' in c.token)&&!('y' in c.token))`) != "true" {
		t.Fatal("property command contains coordinates")
	}
	if eval(browserModuleScenario) != "true" {
		t.Fatal("client reliability modules failed")
	}
	var center struct{ X, Y float64 }
	if err := json.Unmarshal([]byte(eval(browserDraftScenario)), &center); err != nil {
		t.Fatal(err)
	}
	call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": center.X, "y": center.Y, "button": "left", "clickCount": 1})
	call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": center.X, "y": center.Y, "button": "left", "clickCount": 1})
	if eval(`document.querySelector('.token-row.selected')?.dataset.tokenId===window.heroID`) != "true" {
		t.Fatal("list centered on stale token coordinates")
	}
	if eval(`document.getElementById('tokenName').value`) != "\"Unsaved draft\"" {
		t.Fatal("concurrent rename lost")
	}
	eval(`document.getElementById('fit').click();true`)
	if eval(`(()=>{const s=document.getElementById('imageCacheBudget');s.value='128';s.dispatchEvent(new Event('change',{bubbles:true}));return localStorage.getItem('atlas-image-memory-budget-v1')==='128'})()`) != "true" {
		t.Fatal("image cache override was not saved")
	}
	eval(`(()=>{const original=globalThis.createImageBitmap;window.pressureDecodes=0;globalThis.createImageBitmap=async(...args)=>{const header=new DataView(await args[0].slice(0,24).arrayBuffer());if(header.getUint32(16)===512&&header.getUint32(20)===509)window.pressureDecodes++;return original(...args)};return true})()`)
	browserHeavyScene(t, s)
	t.Log("checking 80 native assets above the decoded-image budget")
	eval(`for(let i=0;i<4;i++)document.getElementById('plus').click();true`)
	if eval(`(async()=>{const sleep=ms=>new Promise(r=>setTimeout(r,ms));for(let i=0;i<300;i++){if(performance.getEntriesByType('resource').filter(r=>r.name.includes('/api/asset/')).length>=80&&document.getElementById('diagnostics').textContent.includes('Загрузка 0'))break;await sleep(100)}await sleep(2000);const text=document.getElementById('diagnostics').textContent;const hits=Number(text.match(/кэш (\d+)/)[1]);await sleep(2000);const next=document.getElementById('diagnostics').textContent;if(Number(next.match(/кэш (\d+)/)[1])!==hits)throw Error('visible images keep decoding');if(Number(next.match(/RAM изображений ([\d.]+)/)[1])>128)throw Error('128 MiB RAM budget exceeded');if(performance.getEntriesByType('resource').filter(r=>r.name.includes('/api/asset/')).length<80||!next.includes('ошибки 0'))throw Error('images did not load');if(!next.includes('Загрузка 0'))throw Error('image queue did not settle');return document.querySelectorAll('.token-row').length>=80})()`) != "true" {
		t.Fatal("heavy visible cache scenario failed")
	}
	if eval(`(async()=>{const sleep=ms=>new Promise(r=>setTimeout(r,ms)),s=document.getElementById('imageCacheBudget');s.value='64';s.dispatchEvent(new Event('change',{bubbles:true}));for(let i=0;i<100;i++){await sleep(100);const text=document.getElementById('diagnostics').textContent,m=text.match(/RAM изображений ([\d.]+) \/ (\d+) МБ/);if(m&&Number(m[1])<=64&&Number(m[2])===64&&text.includes('Загрузка 0'))return localStorage.getItem('atlas-image-memory-budget-v1')==='64'}throw Error('cache did not shrink to 64 MiB')})()`) != "true" {
		t.Fatal("lower image cache override was not applied")
	}
	if eval(`(async()=>{const sleep=ms=>new Promise(r=>setTimeout(r,ms)),before=window.pressureDecodes;for(let i=0;i<7;i++)document.getElementById('minus').click();await sleep(2000);for(let i=0;i<7;i++)document.getElementById('plus').click();await sleep(2000);if(before<80||window.pressureDecodes!==before)throw Error('zoom repeatedly decoded retained images');return true})()`) != "true" {
		t.Fatal("zoom did not retain decoded images")
	}
	if eval(`(()=>{const c=document.getElementById('board'),g=c.getContext('2d'),colors=new Set();for(let y=100;y<c.height-100;y+=20)for(let x=100;x<c.width-100;x+=20){colors.add(Array.from(g.getImageData(x,y,1,1).data).join(','))}return colors.size>20})()`) != "true" {
		t.Fatal("Canvas did not render map detail")
	}
	if len(browserErrors) > 0 {
		t.Fatalf("Browser errors: %v", browserErrors)
	}
	if !strings.Contains(result, `"mapLoaded":true`) || !strings.Contains(result, `"tokens":2`) {
		t.Fatal("UI smoke failed")
	}
	screenshot := call("Page.captureScreenshot", map[string]any{"format": "png"})
	if path := os.Getenv("ATLAS_SCREENSHOT"); path != "" {
		var v struct {
			Data string `json:"data"`
		}
		json.Unmarshal(screenshot, &v)
		if e := writeScreenshot(path, v.Data); e != nil {
			t.Fatal(e)
		}
	}
	// Terminal authentication failures must not schedule reconnect attempts.
	for _, missing := range []bool{false, true} {
		badSession := ""
		if missing {
			badSession = "removed-session"
		}
		encoded, _ := json.Marshal(badSession)
		eval(`(()=>{const c=JSON.parse(localStorage.getItem('atlas-sessions'))[0];c.key='invalid';if(` + string(encoded) + `)c.session=` + string(encoded) + `;localStorage.setItem('atlas-sessions',JSON.stringify([c]));return true})()`)
		call("Page.navigate", map[string]any{"url": ts.URL})
		time.Sleep(time.Second)
		if eval(`(async()=>{const Base=WebSocket;window.attempts=0;window.WebSocket=class extends Base{constructor(...args){super(...args);window.attempts++}};document.querySelector('#resumeList button').click();await new Promise(r=>setTimeout(r,3000));return window.attempts===1&&document.getElementById('saveStatus').textContent==='Восстановление остановлено'})()`) != "true" {
			t.Fatal("fatal auth retried")
		}
	}
}
