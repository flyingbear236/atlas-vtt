"""Optional isolated UI smoke: python scripts/preset_editor_ui_smoke.py.
Requires playwright and Chromium; no running Go server or campaign is needed.
"""
import os
from pathlib import Path
from playwright.sync_api import sync_playwright

WEB = Path(__file__).resolve().parent.parent / "web"


def script_source(name):
    return (WEB / name).read_text(encoding="utf-8")


def main():
    characters = script_source("characters.js").replace("export class", "class").replace("export function", "function")
    preset = script_source("preset-editor.js").replace("import {statValueFromInput,statInputValue} from './characters.js';", "").replace("export class PresetEditor", "class PresetEditor")
    definitions = script_source("definitions.js").replace("import {sameValue} from './reliability.js';", "").replace("import {statInputValue,statValueFromInput} from './characters.js';", "").replace("export class DefinitionsEditor", "class DefinitionsEditor").replace("export {installedRuleset,readableError,sourceOf};", "")
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True, executable_path=os.environ.get("ATLAS_CHROME", "/usr/bin/chromium"), args=["--no-sandbox"])
        page = browser.new_page()
        page.goto("about:blank")
        errors = []
        page.on("pageerror", lambda err: errors.append(str(err)))
        page.add_script_tag(content=characters + "\nconst sameValue=(a,b)=>JSON.stringify(a)===JSON.stringify(b);\n" + preset + "\n" + definitions + "\nwindow.PresetEditor=PresetEditor;window.DefinitionsEditor=DefinitionsEditor;")
        result = page.evaluate('''async()=>{
            const data={preset:{id:'wolf',name:'Wolf',kind:'monster',stats:{hp:{type:'integer',value:55},alive:{type:'boolean',value:true},note:{type:'string',value:'original'}},actionIds:['bite'],artwork:''},owner:'monsters.toml',registryRevision:7,linked:2,materialized:1,statDefinitions:{hp:{type:'integer',kind:'resource'},alive:{type:'boolean',kind:'resource'},note:{type:'string',kind:'resource'}},actions:{bite:{name:'Bite'}},artwork:{status:'placeholder'}};
            const submissions=[];let uploadCount=0;
            const editor=new PresetEditor({request:async(path,body)=>{if(body){submissions.push(body);return {registryRevision:8};}if(path.includes('media/assets'))return {assets:[{id:'A'}],rulesetMedia:{}};if(path.includes('media/path'))return {path:'media/wolf.png'};return data;},upload:async()=>{uploadCount++;return {asset:{id:'B'},suggestedPath:'media/new.png'};},thumbnail:async()=>new Blob(['x'],{type:'image/png'}),toast:()=>{},onSaved:()=>{}});
            await editor.open('wolf');
            editor.q('[data-name]').value='Wolf new';
            editor.q('[data-stats] input[data-stat-id="hp"]').value='100';
            editor.q('[data-stats] input[data-stat-id="alive"]').checked=false;
            editor.q('[data-stats] input[data-stat-id="note"]').value='';
            await editor.openPicker();
            await editor.selectAsset('A','wolf.png');
            await editor.save();
            const saved=submissions[0];
            await editor.open('wolf');editor.q('[data-name]').value='Canceled';editor.close();
            const file={name:'owner.toml',source:"schema_version=1\\n[presets.wolf]\\nname='Wolf'\\n",presets:['wolf']};
            const ruleset={registryRevision:7,effective:{stats:{},actions:{},presets:{}},campaign:{stats:{},actions:{},presets:{}},ruleset:{files:{'owner.toml':file},media:{'media/old.png':'A'},registry:{stats:{},actions:{},presets:{wolf:{name:'Wolf'}}}}};
            const preview=[];const mount=document.createElement('div');document.body.append(mount);
            const definitions=new DefinitionsEditor(mount,{load:async()=>ruleset,queue:()=>true,documents:{preview:async(kind,payload)=>{preview.push(payload);return {registryRevision:7,digest:'valid',ruleset:{files:ruleset.ruleset.files,registry:ruleset.ruleset.registry},rulesetDiff:{stats:{},actions:{},presets:{}},artworkWarnings:[]};},operationKey:()=> 'k'},mediaTransport:{upload:async()=>({asset:{id:'B'}}),path:async()=> 'media/new.png'}});
            definitions.registry=ruleset;definitions.setAuthorized(true);definitions.beginRulesetEdit();
            await definitions.addRulesetImages([{name:'new.png',type:'image/png',size:100}]);
            definitions.removeRulesetMedia('media/old.png');await definitions.validateRulesetDraft();
            const draft=structuredClone(definitions.documentState.ruleset.media);
            definitions.cancelRulesetEdit();
            return {saved,uploadCount,submissions:submissions.length,closed:editor.mount.hidden,draft,committed:ruleset.ruleset.media,previewMedia:preview.at(-1).media};
        }''')
        assert result["saved"]["preset"]["stats"] == {"hp": {"type": "integer", "value": 100}, "alive": {"type": "boolean", "value": False}, "note": {"type": "string", "value": ""}}, result
        assert result["saved"]["media"] == {"media/wolf.png": "A"}, result
        assert result["uploadCount"] == 0, result
        assert result["submissions"] == 1 and result["closed"], result
        assert result["draft"] == {"media/new.png": "B"}, result
        assert result["committed"] == {"media/old.png": "A"} and result["previewMedia"] == {"media/new.png": "B"}, result
        assert not errors, errors
        browser.close()
        print("Preset editor and ruleset media draft browser smoke: OK")


if __name__ == "__main__":
    main()
