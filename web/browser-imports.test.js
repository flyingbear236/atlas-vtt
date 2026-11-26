import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,existsSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {execFileSync} from 'node:child_process';

// First check local paths, including literal dynamic imports. Then link the
// static graph with the JavaScript engine without evaluating browser code.
test('browser entrypoint has a complete local JavaScript module graph',()=>{
 const root=new URL('./',import.meta.url),seen=new Set();
 const html=readFileSync(new URL('index.html',root),'utf8');
 const entry=html.match(/<script\b[^>]*type=["']module["'][^>]*src=["']([^"']+)["']/);
 assert.ok(entry,'missing module script in index.html');
 function visit(url){
  if(seen.has(url.href))return;seen.add(url.href);
  assert.ok(existsSync(fileURLToPath(url)),`missing browser module: ${url.pathname}`);
  if(url.pathname.split('/').at(-1).startsWith('vendor-'))return;
  const source=readFileSync(url,'utf8');
  const imports=[...source.matchAll(/(?:\bfrom\s*|\bimport\s*(?:\(\s*)?)["']((?:\.\.?\/|\/)[^"'\r\n]+\.js)["']/g)];
  for(const match of imports){const name=match[1];visit(name.startsWith('/')?new URL(name.slice(1),root):new URL(name,url));}
 }
 visit(new URL(entry[1].replace(/^\//,''),root));
 assert.ok(seen.size>1,'entrypoint module graph was not traversed');
 // SourceTextModule uses the engine's real module linker: syntax errors and
 // missing named/default exports fail even when every file exists. Run in a
 // child to enable VM modules without changing the test runner's invocation.
 const linker = `
  import {SourceTextModule} from 'node:vm';
  import {readFileSync} from 'node:fs';
  const root = new URL(process.argv[1]);
  const modules = new Map();
  function load(url) {
   if (!modules.has(url.href)) modules.set(url.href, new SourceTextModule(readFileSync(url,'utf8'), {identifier:url.href}));
   return modules.get(url.href);
  }
  await load(new URL(process.argv[2],root)).link((name,parent) => {
   if (!name.startsWith('.') && !name.startsWith('/')) throw new Error('Unsupported browser import: '+name+' in '+parent.identifier);
   return load(name.startsWith('/') ? new URL(name.slice(1),root) : new URL(name,parent.identifier));
  });
 `;
 execFileSync(process.execPath,['--experimental-vm-modules','--input-type=module','--eval',linker,root.href,entry[1].replace(/^\//,'')],{encoding:'utf8',timeout:15000});
});
