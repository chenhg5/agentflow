// The run page's canvas: the workflow graph drawn with React Flow, coloured by
// this run's step status. Layout comes from the server (data-flow); React and
// React Flow load from esm.sh, the same versions `agentflow web` uses. Without
// network access the canvas says so and the step list below still works.
const sources = {
  react: "https://esm.sh/react@18.3.1",
  dom: "https://esm.sh/react-dom@18.3.1/client",
  flow: "https://esm.sh/reactflow@11.11.4?deps=react@18.3.1,react-dom@18.3.1",
  css: "https://unpkg.com/reactflow@11.11.4/dist/style.css",
};

let libraries;
function load() {
  if (!libraries) {
    if (!document.querySelector("link[data-reactflow]")) {
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = sources.css;
      link.dataset.reactflow = "";
      document.head.appendChild(link);
    }
    libraries = Promise.all([import(sources.react), import(sources.dom), import(sources.flow)]);
  }
  return libraries;
}

function token(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

async function mount(element) {
  if (element.dataset.mounted) return;
  element.dataset.mounted = "1";
  let data;
  try {
    data = JSON.parse(element.dataset.flow);
  } catch (error) {
    element.textContent = "流程图数据无法解析";
    return;
  }
  let React, ReactDOM, RF;
  try {
    [React, ReactDOM, RF] = await load();
  } catch (error) {
    element.classList.add("flow-offline");
    element.textContent = "画布需要从 esm.sh 加载 React Flow，当前网络加载不到；下面的步骤列表不受影响。";
    return;
  }
  const h = React.createElement;
  const ReactFlow = RF.default;
  const tones = { good: "--good", bad: "--bad", human: "--human", agent: "--agent", warn: "--warn", neutral: "--muted-foreground" };
  const colour = (tone) => token(tones[tone] || "--muted-foreground");

  function Card({ node }) {
    return h("div", { className: "flow-card tone-" + node.tone + (node.end ? " flow-end" : "") },
      h("div", { className: "flow-head" },
        h("i", { className: "flow-dot", style: { background: colour(node.tone) } }),
        h("span", { className: "flow-name", title: node.id }, node.end ? "结束" : node.id)),
      node.end ? null : h("div", { className: "flow-meta" }, [node.executor || "—", node.label].concat(node.badges || []).join(" · ")),
      node.duration || node.attempts > 1 ? h("div", { className: "flow-stats" },
        node.duration ? h("span", null, node.duration) : null,
        node.attempts > 1 ? h("span", null, "×" + node.attempts) : null) : null);
  }

  const nodes = data.nodes.map((node) => ({
    id: node.id,
    position: { x: node.x, y: node.y },
    sourcePosition: RF.Position.Right,
    targetPosition: RF.Position.Left,
    data: { label: h(Card, { node }) },
    style: { borderColor: colour(node.tone) },
    className: "flow-node",
  }));
  const kinds = {
    condition: { stroke: token("--warn"), dash: "5 4" },
    parallel: { stroke: token("--agent"), width: 2 },
    loop: { stroke: token("--human"), dash: "2 4" },
    plain: { stroke: token("--axis") || "#555" },
  };
  const edges = data.edges.map((edge) => {
    const kind = kinds[edge.kind] || kinds.plain;
    return {
      id: edge.id, source: edge.source, target: edge.target, animated: !!edge.animated,
      label: edge.label, labelStyle: { fill: kind.stroke, fontSize: 10 },
      labelBgStyle: { fill: token("--card"), fillOpacity: 1 },
      markerEnd: { type: RF.MarkerType.ArrowClosed, color: kind.stroke },
      style: { stroke: kind.stroke, strokeWidth: kind.width || 1.5, strokeDasharray: kind.dash },
    };
  });
  // A wide graph fitted into the card gets too small to read: then start at a
  // readable zoom on the node that needs attention (waiting, running, failed).
  const attention = data.nodes.find((node) => node.tone === "human") || data.nodes.find((node) => node.tone === "agent")
    || data.nodes.find((node) => node.tone === "bad");
  let instance;
  const focus = () => requestAnimationFrame(() => {
    if (!instance) return;
    instance.fitView({ padding: 0.15 });
    const zoom = instance.getZoom();
    if (attention && zoom < 0.65) instance.setCenter(attention.x + 100, attention.y + 30, { zoom: 0.75 });
    else if (!attention && zoom < 0.4) instance.setCenter(data.nodes[0].x + 100, data.nodes[0].y + 30, { zoom: 0.6 });
  });
  const onInit = (flow) => { instance = flow; focus(); };

  // Height follows the graph, as on Loops: the graph's height at the scale
  // that fits its width, between 200 and 420 px. A short chain gets a short
  // canvas instead of a tall empty box; the fullscreen button shows big ones.
  const size = () => {
    if (element.classList.contains("flow-full")) return;
    const xs = data.nodes.map((node) => node.x), ys = data.nodes.map((node) => node.y);
    const width = Math.max(...xs) - Math.min(...xs) + NODE_WIDTH, height = Math.max(...ys) - Math.min(...ys) + NODE_HEIGHT;
    const room = Math.max(element.clientWidth - 48, 1);
    const scale = Math.max(Math.min(1, room / width), room < 600 ? 0.55 : 0);
    element.style.height = Math.round(Math.min(420, Math.max(200, height * scale + 72))) + "px";
  };
  size();
  // Refit once the canvas has its new size: after a window resize, and after
  // entering or leaving fullscreen (React Flow measures on the next frames).
  let fitTimer;
  new ResizeObserver(() => {
    clearTimeout(fitTimer);
    fitTimer = setTimeout(focus, 60);
  }).observe(element);
  window.addEventListener("resize", size);

  const toggle = () => {
    const full = element.classList.toggle("flow-full");
    if (full) element.style.height = "";
    else size();
    setButton(full);
  };
  let setButton = () => {};
  function FullScreen() {
    const [full, setFull] = React.useState(false);
    setButton = setFull;
    return h("button", { type: "button", className: "flow-full-button", onClick: toggle, title: full ? "退出全屏（Esc）" : "全屏查看" }, full ? "退出全屏" : "全屏");
  }
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && element.classList.contains("flow-full")) toggle();
  });

  element.textContent = "";
  ReactDOM.createRoot(element).render(
    h(ReactFlow, { nodes, edges, onInit, fitView: true, fitViewOptions: { padding: 0.15 }, nodesDraggable: true, proOptions: { hideAttribution: true }, minZoom: 0.2 },
      h(RF.Background, { color: token("--border"), gap: 18 }),
      h(RF.Controls, { showInteractive: false }),
      h(RF.Panel, { position: "top-right" }, h(FullScreen))));
}

const NODE_WIDTH = 200, NODE_HEIGHT = 72;

function mountAll() {
  document.querySelectorAll("[data-flow]").forEach(mount);
}

mountAll();
document.addEventListener("hub:refresh", mountAll);
// Mount any canvas that appears later, whatever replaced the page content:
// the live refresh, or an older cached hub.js that does not send hub:refresh.
new MutationObserver(mountAll).observe(document.body, { childList: true, subtree: true });
