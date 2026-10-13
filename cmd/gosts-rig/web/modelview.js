// A built model (Object Capture's OBJ: its materials and textures) in 3D,
// with three.js: the model viewer (orbit, pan, zoom) and the gallery's
// pictures of models.
//
// Object Capture's models are in meters, y up, standing on y = 0.
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
    } catch (e) { console.warn("model materials:", e); } // (shown gray: OBJ's own)
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
// Editing (level, align, scale): the model under an edit (a matrix, in
// meters, y up); editing() shows the axes and the ground at the origin and
// lets points be picked (onPick, each snapped to the nearest corner of the
// triangle clicked); apply() changes the edit; parts() is the model as
// edited, for saving.
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
  // holder: the edit (its matrix); in it the model as loaded (in meters).
  let holder = null, obj = null, grid = null, axes = null, mode = "textured", running = false, seq = 0, editOn = false, onPick = null, radius = 1;
  const originals = new Map(); // mesh → its own material(s)
  const marks = new THREE.Group();
  marks.renderOrder = 10;
  scene.add(marks);

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
  const drop = (o) => { if (!o) return; scene.remove(o); o.traverse((c) => { c.geometry?.dispose(); c.material?.dispose?.(); }); };
  function clear() {
    running = false;
    seq++;
    if (holder) { scene.remove(holder); disposeModel(holder); holder = obj = null; }
    drop(grid); drop(axes); grid = axes = null;
    markers([]);
    editOn = false;
    originals.clear();
    renderer.clear();
  }
  function look(m) {
    mode = m;
    obj?.traverse((o) => {
      if (!o.isMesh) return;
      o.material = m === "clay" ? CLAY : m === "wire" ? WIRE : originals.get(o);
    });
  }
  function reset() { if (holder) frame(holder, camera, controls); }
  // box: the model's, as edited (each point: exact).
  const box = () => new THREE.Box3().setFromObject(holder, true);
  // ground: the floor grid (and, editing, the axes): under the model as it
  // came, else at the origin (y = 0), a few times its size, lines 1, 2 or
  // 5 × 10ⁿ m apart, about 4 across it.
  function ground() {
    drop(grid); drop(axes); grid = axes = null;
    if (!holder) return;
    const b = box(), r = Math.max(b.getBoundingSphere(new THREE.Sphere()).radius, 1e-3);
    radius = r;
    const raw = r / 2, p = 10 ** Math.floor(Math.log10(raw)), step = (raw / p >= 5 ? 5 : raw / p >= 2 ? 2 : 1) * p;
    const n = Math.ceil(r * 3 / step) * 2;
    grid = new THREE.GridHelper(n * step, n, 0x4a5262, 0x2c323d);
    grid.material.transparent = true;
    grid.material.opacity = 0.7;
    if (editOn) {
      // The axes as Blender and Fusion show them (z up): X red (+x), Y green
      // (−z here: a y-up OBJ's), Z blue (+y), from the origin.
      axes = new THREE.Group();
      const len = n * step / 2;
      for (const [dir, color] of [[[1, 0, 0], 0xe5484d], [[0, 0, -1], 0x46a758], [[0, 1, 0], 0x3e7bfa]]) {
        const g = new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(), new THREE.Vector3(...dir).multiplyScalar(len)]);
        axes.add(new THREE.Line(g, new THREE.LineBasicMaterial({ color, depthTest: false, transparent: true })));
      }
      axes.renderOrder = 5;
      scene.add(axes);
    } else grid.position.set((b.min.x + b.max.x) / 2, b.min.y, (b.min.z + b.max.z) / 2);
    scene.add(grid);
  }
  // show loads the model (base: its folder's address; file: the OBJ;
  // scale: its units in meters, 1 for Object Capture's): its triangles and
  // size once it's in.
  async function show(base, file, status, scale = 1) {
    clear();
    const my = seq;
    const o = await loadOBJ(base, file, status);
    if (my !== seq) { disposeModel(o); return null; } // (closed, or another, meanwhile)
    obj = o;
    obj.scale.setScalar(scale);
    holder = new THREE.Group();
    holder.matrixAutoUpdate = false;
    holder.add(obj);
    obj.traverse((m) => { if (m.isMesh) originals.set(m, m.material); });
    scene.add(holder);
    size();
    frame(holder, camera, controls);
    ground();
    look(mode);
    running = true;
    loop();
    return stats();
  }
  // stats: its triangles and size (m: x, y, z), as edited.
  function stats() { return holder ? { ...modelStats(holder), size: box().getSize(new THREE.Vector3()).toArray() } : null; }

  // ------------------------------------------------------------ editing
  function editing(on) { editOn = on; markers([]); ground(); canvas.style.cursor = on ? "crosshair" : ""; }
  // A click (not a drag) on the model, editing: the triangle's corner
  // nearest it, to onPick.
  const ray = new THREE.Raycaster();
  let down = null;
  canvas.addEventListener("pointerdown", (e) => { down = e.button === 0 ? [e.clientX, e.clientY] : null; });
  canvas.addEventListener("pointerup", (e) => {
    if (!down || !editOn || !onPick || !obj || Math.hypot(e.clientX - down[0], e.clientY - down[1]) > 4) return;
    const b = canvas.getBoundingClientRect();
    ray.setFromCamera(new THREE.Vector2((e.clientX - b.left) / b.width * 2 - 1, -((e.clientY - b.top) / b.height) * 2 + 1), camera);
    const hit = ray.intersectObject(obj, true)[0];
    if (!hit?.face) return;
    const pos = hit.object.geometry.getAttribute("position");
    let best = null;
    for (const i of [hit.face.a, hit.face.b, hit.face.c]) {
      const v = new THREE.Vector3().fromBufferAttribute(pos, i).applyMatrix4(hit.object.matrixWorld);
      if (!best || v.distanceTo(hit.point) < best.distanceTo(hit.point)) best = v;
    }
    onPick(best);
  });
  // markers shows sets of picked points: [{ points, color, line }], each
  // point a dot (seen through the model), line: joined up.
  function markers(sets) {
    for (const c of [...marks.children]) { marks.remove(c); c.geometry.dispose(); c.material.dispose(); }
    const dot = new THREE.SphereGeometry(radius * 0.012, 16, 12);
    for (const s of sets) {
      for (const p of s.points) {
        const m = new THREE.Mesh(dot.clone(), new THREE.MeshBasicMaterial({ color: s.color, depthTest: false, transparent: true }));
        m.position.copy(p);
        m.renderOrder = 10;
        marks.add(m);
      }
      if (s.line && s.points.length > 1) {
        const l = new THREE.Line(new THREE.BufferGeometry().setFromPoints(s.line === "loop" ? [...s.points, s.points[0]] : s.points),
          new THREE.LineBasicMaterial({ color: s.color, depthTest: false, transparent: true }));
        l.renderOrder = 10;
        marks.add(l);
      }
    }
    dot.dispose();
  }
  // The edit: get it, set it, or apply m after it (the view kept).
  const matrix = () => (holder ? holder.matrix.clone() : new THREE.Matrix4());
  function setMatrix(m) {
    if (!holder) return;
    holder.matrix.copy(m);
    holder.matrixWorldNeedsUpdate = true;
    holder.updateMatrixWorld(true);
    ground();
  }
  const apply = (m) => setMatrix(m.clone().multiply(matrix()));
  // boxWith: the model's box were m applied (it isn't).
  function boxWith(m) {
    const was = holder.matrix.clone();
    holder.matrix.premultiply(m);
    holder.updateMatrixWorld(true);
    const b = box();
    holder.matrix.copy(was);
    holder.updateMatrixWorld(true);
    return b;
  }
  const center = () => box().getCenter(new THREE.Vector3());
  // parts: the model as edited, for saving: each mesh's each material's
  // triangles (non-indexed, in meters, y up) and the material's name.
  function parts() {
    const out = [];
    holder.updateMatrixWorld(true);
    obj.traverse((o) => {
      if (!o.isMesh) return;
      const g = o.geometry.index ? o.geometry.toNonIndexed() : o.geometry;
      const mats = [originals.get(o)].flat();
      const n = g.getAttribute("position").count;
      const groups = g.groups.length ? g.groups : [{ start: 0, count: n, materialIndex: 0 }];
      for (const gr of groups) {
        const sub = new THREE.BufferGeometry();
        for (const name of ["position", "normal", "uv"]) {
          const a = g.getAttribute(name);
          if (a) sub.setAttribute(name, new THREE.BufferAttribute(a.array.slice(gr.start * a.itemSize, (gr.start + Math.min(gr.count, n - gr.start)) * a.itemSize), a.itemSize));
        }
        sub.applyMatrix4(o.matrixWorld);
        out.push({ geometry: sub, material: mats[gr.materialIndex ?? 0]?.name || "" });
      }
    });
    return out;
  }
  return {
    show, look, reset, clear, stats, box,
    editing, markers, matrix, setMatrix, apply, boxWith, center, parts,
    set onPick(f) { onPick = f; },
  };
}

// The gallery's pictures of models: one renderer, one model at a time.
let thumbR = null, thumbQueue = Promise.resolve();
// modelThumb renders the OBJ at base (file) as a w×h JPEG on bg (a CSS color).
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
