// A built model (Object Capture's OBJ: its materials and textures) in 3D,
// with three.js: the model viewer (orbit, pan, zoom) and the gallery's
// pictures of models.
//
// Object Capture's models are in metres, y up, standing on y = 0.
import * as THREE from "three";
import { OrbitControls } from "./vendor/OrbitControls.js";
import { OBJLoader } from "./vendor/OBJLoader.js";
import { MTLLoader } from "./vendor/MTLLoader.js";

// fetchText is url's text; progress(loaded, total) as it comes.
async function fetchText(url, progress) {
  const r = await fetch(url, { cache: "no-cache" });
  if (!r.ok) throw new Error(`${url}: ${r.status} ${(await r.text()).trim()}`);
  const total = Number(r.headers.get("Content-Length")) || 0;
  if (!r.body || !progress) return r.text();
  const reader = r.body.getReader(), parts = [];
  let loaded = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
    loaded += value.length;
    progress(loaded, total);
  }
  return new TextDecoder().decode(await new Blob(parts).arrayBuffer());
}

// loadOBJ loads the OBJ file at base (a folder's address, ending in /), its
// materials and their textures (all in): the model, as a group.
// status(text) says how it's going.
export async function loadOBJ(base, file, status = () => {}) {
  const mb = (n) => (n / 1e6).toFixed(n < 1e7 ? 1 : 0);
  const text = await fetchText(base + encodeURIComponent(file), (got, total) =>
    status(total ? `Loading the mesh: ${mb(got)} of ${mb(total)} MB` : `Loading the mesh: ${mb(got)} MB`));
  // The textures: counted in and out (the manager's), so it's whole when shown.
  let pending = 0, settle;
  const allIn = new Promise((r) => (settle = r));
  const manager = new THREE.LoadingManager();
  const start = manager.itemStart.bind(manager), end = manager.itemEnd.bind(manager);
  manager.itemStart = (u) => { pending++; start(u); };
  manager.itemEnd = (u) => { end(u); if (--pending === 0) settle(); };
  const loader = new OBJLoader();
  const mtl = /^mtllib\s+(.+?)\s*$/m.exec(text)?.[1];
  if (mtl) {
    status("Loading the materials");
    try {
      const mats = await new MTLLoader(manager).setPath(base).loadAsync(encodeURIComponent(mtl));
      mats.preload();
      loader.setMaterials(mats);
    } catch (e) { console.warn("model materials:", e); } // (shown grey: OBJ's own)
  }
  status("Reading the mesh");
  await new Promise((r) => setTimeout(r)); // (the status shown before the parse holds the page)
  const obj = loader.parse(text);
  if (pending > 0) { status("Loading the textures"); await allIn; }
  obj.traverse((o) => {
    if (!o.isMesh) return;
    if (!o.geometry.getAttribute("normal")) o.geometry.computeVertexNormals();
    for (const m of [o.material].flat()) if (m) m.side = THREE.DoubleSide; // (a scan's open edges: seen from inside too)
  });
  return obj;
}

// modelStats: its triangles and size (m: x, y, z).
function modelStats(obj) {
  let triangles = 0;
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const g = o.geometry;
    triangles += (g.index ? g.index.count : g.getAttribute("position").count) / 3;
  });
  const size = new THREE.Box3().setFromObject(obj).getSize(new THREE.Vector3());
  return { triangles, size: [size.x, size.y, size.z] };
}

// disposeModel frees the model's geometry, materials and textures.
function disposeModel(obj) {
  obj?.traverse((o) => {
    if (!o.isMesh) return;
    o.geometry.dispose();
    for (const m of [o.material].flat()) {
      if (!m) continue;
      for (const v of Object.values(m)) if (v?.isTexture) v.dispose();
      m.dispose();
    }
  });
}

// The camera's way in: from the front, a little right and above.
const VIEW_DIR = new THREE.Vector3(0.55, 0.45, 1).normalize();

// frame puts camera (and controls, if any) on obj, just all of it in view
// (each point: as far off as it needs, at the camera's angle, from the
// middle of its box; margin: a fraction of the view left round it).
function frame(obj, camera, controls, margin = 0.06) {
  const box = new THREE.Box3().setFromObject(obj);
  const c = box.getCenter(new THREE.Vector3());
  const r = Math.max(box.getBoundingSphere(new THREE.Sphere()).radius, 1e-3);
  const fwd = VIEW_DIR.clone(), right = new THREE.Vector3().crossVectors(new THREE.Vector3(0, 1, 0), fwd).normalize();
  const up = new THREE.Vector3().crossVectors(fwd, right);
  const tanV = Math.tan(THREE.MathUtils.degToRad(camera.fov) / 2) * (1 - margin), tanH = tanV * camera.aspect;
  let dist = 0;
  const p = new THREE.Vector3();
  obj.updateMatrixWorld(true);
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const pos = o.geometry.getAttribute("position");
    for (let i = 0; i < pos.count; i++) {
      p.fromBufferAttribute(pos, i).applyMatrix4(o.matrixWorld).sub(c);
      const near = p.dot(fwd); // (towards the camera)
      dist = Math.max(dist, near + Math.abs(p.dot(right)) / tanH, near + Math.abs(p.dot(up)) / tanV);
    }
  });
  dist = dist || r * 3; // (no points)
  camera.position.copy(c).addScaledVector(fwd, dist);
  camera.near = dist / 1000; // (zoomed right in: not cut)
  camera.far = dist * 20 + r * 20; // (zoomed out as far as it goes)
  camera.updateProjectionMatrix();
  camera.lookAt(c);
  if (controls) {
    controls.target.copy(c);
    controls.minDistance = r * 0.05;
    controls.maxDistance = dist * 10;
    controls.update();
  }
  return { box, r };
}

// lights: soft all round (sky and ground), a lamp at the camera.
function addLights(scene, camera) {
  scene.add(new THREE.HemisphereLight(0xffffff, 0x8a8f99, 3));
  const lamp = new THREE.DirectionalLight(0xffffff, 1.6);
  lamp.position.set(0.3, 0.6, 1);
  camera.add(lamp);
  scene.add(camera);
}

const CLAY = new THREE.MeshStandardMaterial({ color: 0xb9bdc5, roughness: 0.85, metalness: 0, side: THREE.DoubleSide });
const WIRE = new THREE.MeshBasicMaterial({ color: 0x7ea6ff, wireframe: true });

// createModelView: the viewer on canvas (in a box that sizes it). show()
// loads a model in place of the one before; look() textured | clay | wire;
// reset() the view as it came; clear() frees it (the viewer closed).
export function createModelView(canvas) {
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(35, 1, 0.01, 100);
  addLights(scene, camera);
  const controls = new OrbitControls(camera, canvas);
  controls.enableDamping = true;
  controls.dampingFactor = 0.12;
  controls.screenSpacePanning = true;
  controls.zoomToCursor = true;
  let model = null, grid = null, mode = "textured", running = false, seq = 0;
  const originals = new Map(); // mesh → its own material(s)

  function size() {
    const box = canvas.parentElement, w = box.clientWidth, h = box.clientHeight;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  }
  new ResizeObserver(size).observe(canvas.parentElement);
  function loop() {
    if (!running) return;
    controls.update();
    renderer.render(scene, camera);
    requestAnimationFrame(loop);
  }
  function clear() {
    running = false;
    seq++;
    if (model) { scene.remove(model); disposeModel(model); model = null; }
    if (grid) { scene.remove(grid); grid.geometry.dispose(); grid.material.dispose(); grid = null; }
    originals.clear();
    renderer.clear();
  }
  function look(m) {
    mode = m;
    model?.traverse((o) => {
      if (!o.isMesh) return;
      o.material = m === "clay" ? CLAY : m === "wire" ? WIRE : originals.get(o);
    });
  }
  function reset() { if (model) frame(model, camera, controls); }
  // show loads the model (base: its folder's address; file: the OBJ):
  // its triangles and size once it's in.
  async function show(base, file, status) {
    clear();
    const my = seq;
    const obj = await loadOBJ(base, file, status);
    if (my !== seq) { disposeModel(obj); return null; } // (closed, or another, meanwhile)
    model = obj;
    obj.traverse((o) => { if (o.isMesh) originals.set(o, o.material); });
    scene.add(obj);
    size();
    const { box, r } = frame(obj, camera, controls);
    // A floor grid under it, a few times its size: lines 1, 2 or 5 × 10ⁿ m
    // apart, about 4 across it.
    const raw = r / 2, p = 10 ** Math.floor(Math.log10(raw)), step = (raw / p >= 5 ? 5 : raw / p >= 2 ? 2 : 1) * p;
    const n = Math.ceil(r * 3 / step) * 2;
    grid = new THREE.GridHelper(n * step, n, 0x4a5262, 0x2c323d);
    grid.material.transparent = true;
    grid.material.opacity = 0.7;
    grid.position.set((box.min.x + box.max.x) / 2, box.min.y, (box.min.z + box.max.z) / 2);
    scene.add(grid);
    look(mode);
    running = true;
    loop();
    return modelStats(obj);
  }
  return { show, look, reset, clear };
}

// The gallery's pictures of models: one renderer, one model at a time.
let thumbR = null, thumbQueue = Promise.resolve();
// modelThumb renders the OBJ at base (file) as a w×h JPEG on bg (a CSS colour).
export function modelThumb(base, file, w, h, bg) {
  const job = thumbQueue.then(async () => {
    if (!thumbR) thumbR = new THREE.WebGLRenderer({ antialias: true, preserveDrawingBuffer: true });
    thumbR.setPixelRatio(1);
    thumbR.setSize(w, h, false);
    const scene = new THREE.Scene();
    scene.background = new THREE.Color(bg);
    const camera = new THREE.PerspectiveCamera(35, w / h, 0.01, 100);
    addLights(scene, camera);
    const obj = await loadOBJ(base, file);
    try {
      scene.add(obj);
      frame(obj, camera);
      thumbR.render(scene, camera);
      return await new Promise((r) => thumbR.domElement.toBlob(r, "image/jpeg", 0.85));
    } finally {
      disposeModel(obj);
    }
  });
  thumbQueue = job.catch(() => {});
  return job;
}
