const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const path = require('node:path');
const html = fs.readFileSync(path.join(__dirname, '../internal/web/ui/index.html'), 'utf8');
const source = html.match(/<script type="module">([\s\S]*?)<\/script>/)[1].replace(/^\s*import .*;$/gm, '').replace(/createRoot\(document.getElementById\("root"\)\).render\(h\(App\)\);/, '');
function env(saved, languages) {
  const context = { React: { createElement: (type,props,...children) => ({type,props,children}) }, useState: value => [typeof value === 'function' ? value() : value, () => {}], useEffect: () => {}, useRef: () => ({current:null}), navigator: { languages, language: languages[0] }, localStorage: {getItem: () => saved} };
  vm.createContext(context); vm.runInContext(source,context); return context;
}
for (const [saved, langs, expected] of [[null,['zh-CN'],'zh-CN'],[null,['zh-Hant-HK'],'zh-TW'],[null,['zh-SG'],'zh-CN'],[null,['de-DE','en-GB'],'en'],['zh-TW',['en-US'],'zh-TW'],['en',['zh-CN'],'en'],['unsupported',['zh-Hans'],'zh-CN']]) {
  assert.equal(env(saved,langs).initialLanguage(),expected);
}
const c=env(null,['zh-CN']);
assert.doesNotThrow(() => c.NodeCard({definition:{id:'example',execution:{candidates:[{executor:'codex'}]}},state:{attempts:1},status:'running',language:'zh-CN'}));
for (const value of [{}, [], false, 0, null, '']) {
  const rendered = c.RuntimeValue({label:'Input',record:{available:true,value},language:'zh-CN'});
  assert.equal(rendered.children[1].type,'pre');
  assert.equal(rendered.children[1].children[0],JSON.stringify(value,null,2));
}
const inspector=c.Inspector({definition:{id:'example'},state:{status:'running',input:{}},runID:'test',language:'zh-CN'});
assert.ok(inspector);
console.log('PASS: browser locales, saved language priority, NodeCard rendering, empty/falsy JSON, running inspector');
