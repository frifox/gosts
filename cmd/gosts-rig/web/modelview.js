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

// VIEWS: where the camera looks from (towards the model), in Blender's and
// Fusion's axes (X red, Y green, Z blue up; here x, −z, y):
//   front: X right, Z up, Y going away (from −Y);
//   side:  Y right, Z up, X coming towards it (from +X);
//   top:   Y right, X down, Z towards it (from above, X at the bottom);
//   3d:    Z up, X right and away, Y left and away: from the front (−Y),
//          turned 30° towards −X, 15° above.
export const VIEWS = {
  "3d": new THREE.Vector3(-Math.sin(30 * Math.PI / 180) * Math.cos(15 * Math.PI / 180), Math.sin(15 * Math.PI / 180), Math.cos(30 * Math.PI / 180) * Math.cos(15 * Math.PI / 180)),
  front: new THREE.Vector3(0, 0, 1),
  side: new THREE.Vector3(1, 0, 0),
  top: new THREE.Vector3(0.001, 1, 0).normalize(), // (the slight +x: the screen's up is −x, X pointing down)
};
const VIEW_DIR = VIEWS["3d"];
// The margin round the model framed (a fraction of the view): FIT, as it
// opens (about 80% of the view), FILL, as big as it goes (F).
const FIT = 0.2, FILL = 0.06;

// frame puts camera (and controls, if any) on obj, just all of it in view
// (each point: as far off as it needs, at the camera's angle, from the
// middle of its box; margin: a fraction of the view left round it).
function frame(obj, camera, controls, margin = 0.06, dir = VIEW_DIR, aspect = camera.aspect) {
  const box = new THREE.Box3().setFromObject(obj);
  const c = box.getCenter(new THREE.Vector3());
  const r = Math.max(box.getBoundingSphere(new THREE.Sphere()).radius, 1e-3);
  const fwd = dir.clone(), right = new THREE.Vector3().crossVectors(new THREE.Vector3(0, 1, 0), fwd).normalize();
  const up = new THREE.Vector3().crossVectors(fwd, right);
  const tanV = Math.tan(THREE.MathUtils.degToRad(camera.fov) / 2) * (1 - margin), tanH = tanV * aspect;
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
  renderer.localClippingEnabled = true;
  const scene = new THREE.Scene();
  // Trimming: the box's six sides as clipping planes (inwards; off: far
  // out), on every material the model's shown with; the box drawn.
  const FAR = 1e9;
  const planes = [[1, 0, 0], [-1, 0, 0], [0, 1, 0], [0, -1, 0], [0, 0, 1], [0, 0, -1]].map((n) => new THREE.Plane(new THREE.Vector3(...n), FAR));
  for (const m of [CLAY, WIRE]) m.clippingPlanes = planes;
  let boxHelper = null;
  const camera = new THREE.PerspectiveCamera(35, 1, 0.01, 100);
  addLights(scene, camera);
  const controls = new OrbitControls(camera, canvas);
  controls.enableDamping = true;
  controls.dampingFactor = 0.12;
  controls.screenSpacePanning = true;
  // A drag moves the view; the wheel pressed and dragged (or the right
  // button) turns it; the wheel turned zooms.
  controls.mouseButtons.LEFT = THREE.MOUSE.PAN;
  controls.mouseButtons.MIDDLE = THREE.MOUSE.ROTATE;
  controls.mouseButtons.RIGHT = THREE.MOUSE.ROTATE;
  // ⇧ with the wheel zooms: pressed and dragged (up and down), or turned —
  // macOS makes a ⇧ scroll a sideways one, which the controls don't zoom by,
  // so that's sent on as an up-and-down one. (Seen before the controls: on
  // the canvas, as it's captured.)
  canvas.addEventListener("pointerdown", (e) => {
    controls.mouseButtons.MIDDLE = e.button === 1 && e.shiftKey ? THREE.MOUSE.DOLLY : THREE.MOUSE.ROTATE;
  }, { capture: true });
  canvas.addEventListener("wheel", (e) => {
    if (!e.shiftKey || e.deltaY || !e.deltaX) return;
    e.stopImmediatePropagation();
    e.preventDefault();
    canvas.dispatchEvent(new WheelEvent("wheel", { deltaY: e.deltaX, deltaMode: e.deltaMode, clientX: e.clientX, clientY: e.clientY, bubbles: true, cancelable: true }));
  }, { capture: true, passive: false });
  controls.zoomToCursor = true;
  // holder: the edit (its matrix); in it the model as loaded (in meters).
  let viewName = "3d", holder = null, obj = null, grid = null, axes = null, mode = "textured", running = false, seq = 0, editOn = false, onPick = null, radius = 1;
  const originals = new Map(); // mesh → its own material(s)
  const marks = new THREE.Group();
  marks.renderOrder = 10;
  scene.add(marks);
  // ball: a marker, a unit ball (scaled to the model): lit, where it's seen;
  // a faint flat copy over it where the model's in front (so never lost).
  const BALL = new THREE.SphereGeometry(1, 32, 24);
  // Its shading: a matcap (a lit ball, drawn: a bright spot up left, the
  // edge darker), gray, tinted by each ball's color; the same from any view,
  // whatever lights the model.
  const BALL_SHADE = (() => {
    const n = 256, cv = document.createElement("canvas");
    cv.width = cv.height = n;
    const x = cv.getContext("2d");
    const body = x.createRadialGradient(n * 0.38, n * 0.34, n * 0.02, n * 0.5, n * 0.5, n * 0.5);
    body.addColorStop(0, "#ffffff");
    body.addColorStop(0.18, "#e6e6e6");
    body.addColorStop(0.55, "#9a9a9a");
    body.addColorStop(0.85, "#555555");
    body.addColorStop(1, "#2e2e2e");
    x.fillStyle = body;
    x.fillRect(0, 0, n, n);
    const spot = x.createRadialGradient(n * 0.34, n * 0.3, 0, n * 0.34, n * 0.3, n * 0.12); // (the highlight: sharp)
    spot.addColorStop(0, "rgba(255,255,255,0.95)");
    spot.addColorStop(1, "rgba(255,255,255,0)");
    x.fillStyle = spot;
    x.fillRect(0, 0, n, n);
    const t = new THREE.CanvasTexture(cv);
    t.colorSpace = THREE.SRGBColorSpace;
    return t;
  })();
  function ball(color, opacity = 1) {
    const g = new THREE.Group();
    const lit = new THREE.Mesh(BALL, new THREE.MeshMatcapMaterial({ color: new THREE.Color(color).lerp(new THREE.Color(0xffffff), 0.15), matcap: BALL_SHADE,
      transparent: opacity < 1, opacity }));
    const ghost = new THREE.Mesh(BALL, new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 0.3 * opacity, depthTest: false, depthWrite: false }));
    ghost.renderOrder = 9;
    lit.renderOrder = 10;
    g.add(ghost, lit);
    return g;
  }
  // The hover ball: where a click would put a marker (see pickAt).
  let hover = ball(0xffffff, 0.55);
  hover.visible = false;
  scene.add(hover);
  function hoverColor(color) {
    hover.children[0].material.color.set(color);
    hover.children[1].material.color.set(color).lerp(new THREE.Color(0xffffff), 0.15);
  }

  // inset: the stage's right side covered (the Edit panel), px; the
  // picture shifted left by half of it (eased there), so the model's in
  // the middle of what's seen. shift: where the shift is now.
  let inset = 0, shift = 0;
  function size() {
    const box = canvas.parentElement, w = box.clientWidth, h = box.clientHeight;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    offset();
  }
  function offset() {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (shift > 0.5 && w && h) camera.setViewOffset(w, h, shift, 0, w, h);
    else camera.clearViewOffset();
    camera.updateProjectionMatrix();
  }
  function shiftTick(dt) {
    const want = inset / 2;
    if (Math.abs(want - shift) < 0.5) { if (shift !== want) { shift = want; offset(); } return; }
    shift += (want - shift) * Math.min(1, dt * 12);
    offset();
  }
  // seen: the part of the stage not covered, as the camera's aspect (what
  // framing fits the model to).
  const seenAspect = () => Math.max(0.2, (canvas.clientWidth - inset) / Math.max(1, canvas.clientHeight));
  function setInset(px) { inset = Math.max(0, px); }
  new ResizeObserver(size).observe(canvas.parentElement);
  let last = 0;
  function loop(now = performance.now()) {
    if (!running) return;
    const dt = Math.min(0.1, (now - (last || now)) / 1000);
    glideTick(dt);
    tweenTick(dt);
    shiftTick(dt);


    last = now;
    controls.update();
    renderer.render(scene, camera);
    requestAnimationFrame(loop);
  }
  const drop = (o) => { if (!o) return; scene.remove(o); o.traverse((c) => { c.geometry?.dispose(); c.material?.dispose?.(); }); };
  function clear() {
    running = false;
    glideTo = null;
    tweenEnd();
    target = null;
    seq++;
    if (holder) { scene.remove(holder); disposeModel(holder); holder = obj = null; }
    drop(grid); drop(axes); grid = axes = null;
    clip(null);
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
  // view: the camera to a preset (VIEWS), all of the model in sight.
  // glide: over about 0.7 s instead of at once, swinging around the model
  // (as the Rig View does); the view turned by hand stops it.
  // fill: the model as big as the view takes it (F), else about 80% of it.
  function view(name, glide = false, fill = false) {
    if (VIEWS[name]) viewName = name;
    if (!holder) return;
    const from = camera.position.clone(), fromT = controls.target.clone();
    frame(holder, camera, controls, fill ? FILL : FIT, VIEWS[viewName], seenAspect()); // (where it ends: its near, far and zoom limits too)
    glideTo = null;
    if (!glide) return;
    glideTo = { from, fromT, to: camera.position.clone(), toT: controls.target.clone(), age: 0 };
    glideTick(0);
  }
  const reset = (glide = false) => view("3d", glide);
  let glideTo = null;
  controls.addEventListener("start", () => (glideTo = null));
  // glideTick moves the view along a glide (eased), till it's there.
  function glideTick(dt) {
    if (!glideTo) return;
    glideTo.age += dt;
    let f = Math.min(1, glideTo.age / 0.7);
    f = f * f * (3 - 2 * f);
    // Around the target (angles and distance eased), not straight across.
    const a = new THREE.Spherical().setFromVector3(glideTo.from.clone().sub(glideTo.fromT));
    const b = new THREE.Spherical().setFromVector3(glideTo.to.clone().sub(glideTo.toT));
    let dTheta = b.theta - a.theta;
    dTheta -= Math.round(dTheta / (2 * Math.PI)) * 2 * Math.PI; // the short way round
    const sp = new THREE.Spherical(a.radius + (b.radius - a.radius) * f, a.phi + (b.phi - a.phi) * f, a.theta + dTheta * f);
    controls.target.lerpVectors(glideTo.fromT, glideTo.toT, f);
    camera.position.setFromSpherical(sp).add(controls.target);
    camera.lookAt(controls.target);
    if (f >= 1) glideTo = null;
  }
  // box: the model's, as edited (each point: exact).
  const box = () => new THREE.Box3().setFromObject(holder, true);
  // ground: the floor grid (and, editing, the axes): under the model as it
  // came, else at the origin (y = 0), a few times its size, lines 1, 2 or
  // 5 × 10ⁿ m apart, about 4 across it.
  // ground: the floor grid (the ground: y = 0, about the origin) and the
  // axes from the origin, as Blender and Fusion show them (z up): X red
  // (+x), Y green (−z here: a y-up OBJ's), Z blue (+y); each shown as set
  // (show()). A few times the model's size, lines 1, 2 or 5 × 10ⁿ m apart.
  let showAxes = true, showGround = true;
  function ground() {
    drop(grid); drop(axes); grid = axes = null;
    if (!holder) return;
    const b = box(), r = Math.max(b.getBoundingSphere(new THREE.Sphere()).radius, 1e-3);
    radius = r;
    const reach = Math.max(r * 1.5, Math.abs(b.min.x), Math.abs(b.max.x), Math.abs(b.min.z), Math.abs(b.max.z)) * 1.1; // (off the origin too)
    const raw = r / 2, p = 10 ** Math.floor(Math.log10(raw)), step = (raw / p >= 5 ? 5 : raw / p >= 2 ? 2 : 1) * p;
    const n = Math.ceil(reach / step) * 2;
    grid = new THREE.GridHelper(n * step, n, 0x4a5262, 0x2c323d);
    grid.material.transparent = true;
    grid.material.opacity = 0.7;
    grid.visible = showGround;
    scene.add(grid);
    axes = new THREE.Group();
    const len = n * step / 2;
    for (const [dir, color] of [[[1, 0, 0], 0xe5484d], [[0, 0, -1], 0x46a758], [[0, 1, 0], 0x3e7bfa]]) {
      const g = new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(), new THREE.Vector3(...dir).multiplyScalar(len)]);
      axes.add(new THREE.Line(g, new THREE.LineBasicMaterial({ color, depthTest: false, transparent: true })));
    }
    axes.renderOrder = 5;
    axes.visible = showAxes;
    scene.add(axes);
  }
  // shown: the axes and the ground, each on or off.
  function shown({ axes: a = showAxes, ground: g = showGround } = {}) {
    showAxes = a; showGround = g;
    if (axes) axes.visible = a;
    if (grid) grid.visible = g;
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
    obj.traverse((m) => {
      if (!m.isMesh) return;
      originals.set(m, m.material);
      for (const mat of [m.material].flat()) if (mat) mat.clippingPlanes = planes;
    });
    scene.add(holder);
    viewName = "3d";
    size();
    frame(holder, camera, controls, FIT, VIEW_DIR, seenAspect());
    ground();
    look(mode);
    running = true;
    loop();
    return stats();
  }
  // stats: its triangles and size (m: x, y, z), as edited.
  function stats() { return holder ? { ...modelStats(holder), size: box().getSize(new THREE.Vector3()).toArray() } : null; }

  // ------------------------------------------------------------ editing
  function editing(on) { editOn = on; ground(); }
  // A click (not a drag) on the model, while picking (onPick set: Edit's
  // steps, Measure): the triangle's corner nearest it, to onPick.
  const ray = new THREE.Raycaster();
  let down = null;
  // pickAt: the corner (of the triangle under the pointer at x, y) nearest
  // it, or null (not over the model).
  function pickAt(x, y) {
    if (!obj) return null;
    const b = canvas.getBoundingClientRect();
    ray.setFromCamera(new THREE.Vector2((x - b.left) / b.width * 2 - 1, -((y - b.top) / b.height) * 2 + 1), camera);
    const hit = ray.intersectObject(obj, true)[0];
    if (!hit?.face) return null;
    const pos = hit.object.geometry.getAttribute("position");
    let best = null;
    for (const i of [hit.face.a, hit.face.b, hit.face.c]) {
      const v = new THREE.Vector3().fromBufferAttribute(pos, i).applyMatrix4(hit.object.matrixWorld);
      if (!best || v.distanceTo(hit.point) < best.distanceTo(hit.point)) best = v;
    }
    return best;
  }
  canvas.addEventListener("pointerdown", (e) => { down = e.button === 0 ? [e.clientX, e.clientY] : null; });
  canvas.addEventListener("pointerup", (e) => {
    if (!down || !onPick || Math.hypot(e.clientX - down[0], e.clientY - down[1]) > 4) return;
    const p = pickAt(e.clientX, e.clientY);
    if (p) onPick(p);
  });
  // While picking: a ball where a click would put one (the corner it'd
  // snap to), following the pointer; not while a button's held (turning).
  let hoverAt = null;
  canvas.addEventListener("pointermove", (e) => {
    if (!onPick || e.buttons) { if (hover.visible) hover.visible = false; return; }
    if (!hoverAt) requestAnimationFrame(() => {
      const p = onPick && hoverAt ? pickAt(hoverAt[0], hoverAt[1]) : null;
      hoverAt = null;
      hover.visible = !!p;
      if (p) { hover.position.copy(p); hover.scale.setScalar(radius * 0.016); }
    });
    hoverAt = [e.clientX, e.clientY];
  });
  canvas.addEventListener("pointerleave", () => { hover.visible = false; });
  // markers shows sets of picked points: [{ points, color, line }], each
  // point a shaded ball (behind the model, still seen: faintly), line:
  // joined up.
  function markers(sets) {
    for (const c of [...marks.children]) { marks.remove(c); c.traverse((o) => { if (o.geometry !== BALL) o.geometry?.dispose(); o.material?.dispose(); }); }
    for (const s of sets) {
      for (const p of s.points) {
        const m = ball(s.color);
        m.position.copy(p);
        m.scale.setScalar(radius * 0.016);
        marks.add(m);
      }
      if (s.line && s.points.length > 1) {
        const l = new THREE.Line(new THREE.BufferGeometry().setFromPoints(s.line === "loop" ? [...s.points, s.points[0]] : s.points),
          new THREE.LineBasicMaterial({ color: s.color, depthTest: false, transparent: true }));
        l.renderOrder = 10;
        marks.add(l);
      }
    }
  }
  // The edit: get it, set it, or apply m after it (the view kept). With
  // animate, the model moves there over about a third of a second (eased).
  // A move started while one's going takes over from wherever the model is
  // just then (no jump: it heads for the new place from there; the one
  // before's promise done). target: where it's headed (the edit, as far as
  // the rest knows).
  let target = null, tween = null;
  const matrix = () => (holder ? (target || holder.matrix).clone() : new THREE.Matrix4());
  function place(m) {
    holder.matrix.copy(m);
    holder.matrixWorldNeedsUpdate = true;
    holder.updateMatrixWorld(true);
  }
  const decomp = (x) => { const p = new THREE.Vector3(), q = new THREE.Quaternion(), sc = new THREE.Vector3(); x.decompose(p, q, sc); return { p, q, sc }; };
  function setMatrix(m, animate = false) {
    if (!holder) return Promise.resolve();
    if (!animate) { tweenEnd(); target = m.clone(); place(m); ground(); return Promise.resolve(); }
    if (tween) tween.done(); // (taken over: from where it's got to)
    target = m.clone();
    return new Promise((done) => { tween = { from: decomp(holder.matrix), to: decomp(m), age: 0, done }; });
  }
  const apply = (m, animate = false) => setMatrix(m.clone().multiply(matrix()), animate);
  function tweenTick(dt) {
    if (!tween) return;
    tween.age += dt;
    let f = Math.min(1, tween.age / 0.35);
    f = f * f * (3 - 2 * f);
    const { from: a, to: b } = tween;
    place(new THREE.Matrix4().compose(a.p.clone().lerp(b.p, f), a.q.clone().slerp(b.q, f), a.sc.clone().lerp(b.sc, f)));
    if (f >= 1) tweenEnd();
  }
  // tweenEnd: a move going on, there at once.
  function tweenEnd() {
    if (!tween) return;
    const t = tween;
    tween = null;
    if (holder) { place(target); ground(); }
    t.done();
  }
  // boxWith: the model's box were m applied (it isn't).
  function boxWith(m) {
    const was = holder.matrix.clone();
    place(m.clone().multiply(matrix())); // (from where it's going, if moving)
    const b = box();
    place(was);
    return b;
  }
  const center = () => box().getCenter(new THREE.Vector3());
  // clip shows the model as trimmed to b (a Box3, as edited), the box
  // drawn; null: whole.
  function clip(b) {
    if (boxHelper) { scene.remove(boxHelper); boxHelper.geometry.dispose(); boxHelper.material.dispose(); boxHelper = null; }
    const c = b ? [-b.min.x, b.max.x, -b.min.y, b.max.y, -b.min.z, b.max.z] : Array(6).fill(FAR);
    planes.forEach((p, i) => (p.constant = c[i]));
    if (!b) return;
    boxHelper = new THREE.Box3Helper(b.clone(), 0xffb020);
    boxHelper.material.depthTest = false;
    boxHelper.material.transparent = true;
    boxHelper.renderOrder = 9;
    scene.add(boxHelper);
  }
  // trim cuts the model to b (a Box3, as edited): triangles inside kept,
  // outside dropped, across a side cut along it (their corners there
  // interpolated: texture and normals too). A function that puts it back,
  // or an error if nothing'd be left.
  function trim(b) {
    holder.updateMatrixWorld(true);
    // The sides, inwards: inside where each is ≥ 0.
    const sides = [[1, 0, 0, -b.min.x], [-1, 0, 0, b.max.x], [0, 1, 0, -b.min.y], [0, -1, 0, b.max.y], [0, 0, 1, -b.min.z], [0, 0, -1, b.max.z]];
    const dist = (w, [x, y, z, c]) => x * w.x + y * w.y + z * w.z + c;
    const swaps = [];
    let kept = 0;
    obj.traverse((o) => {
      if (!o.isMesh) return;
      const g = o.geometry.index ? o.geometry.toNonIndexed() : o.geometry;
      const names = Object.keys(g.attributes), attrs = names.map((n) => g.getAttribute(n));
      const pos = g.getAttribute("position"), n = pos.count;
      const groups = g.groups.length ? g.groups : [{ start: 0, count: n, materialIndex: 0 }];
      const out = names.map(() => []), ng = [];
      // A corner: where it is (as edited) and its attributes (as stored).
      const corner = (i) => ({ w: new THREE.Vector3().fromBufferAttribute(pos, i).applyMatrix4(o.matrixWorld), a: attrs.map((at) => Array.from(at.array.subarray(i * at.itemSize, (i + 1) * at.itemSize))) });
      const lerp = (p, q, t) => ({ w: p.w.clone().lerp(q.w, t), a: p.a.map((v, k) => v.map((x, j) => x + (q.a[k][j] - x) * t)) });
      const emit = (c) => c.a.forEach((v, k) => out[k].push(...v));
      let tris = 0;
      const w = [new THREE.Vector3(), new THREE.Vector3(), new THREE.Vector3()];
      for (const gr of groups) {
        const before = tris;
        for (let t = gr.start; t + 2 < Math.min(gr.start + gr.count, n); t += 3) {
          // Most are wholly in (copied as they are) or wholly out past a side.
          for (let k = 0; k < 3; k++) w[k].fromBufferAttribute(pos, t + k).applyMatrix4(o.matrixWorld);
          if (w.every((v) => b.containsPoint(v))) {
            attrs.forEach((at, k) => { const sz = at.itemSize; for (let i = t * sz; i < (t + 3) * sz; i++) out[k].push(at.array[i]); });
            tris++;
            continue;
          }
          if (sides.some((sd) => w.every((v) => dist(v, sd) < 0))) continue;
          let poly = [corner(t), corner(t + 1), corner(t + 2)];
          for (const sd of sides) {
            const d = poly.map((c) => dist(c.w, sd));
            if (d.every((x) => x >= 0)) continue;
            if (d.every((x) => x < 0)) { poly = []; break; }
            const next = [];
            for (let i = 0; i < poly.length; i++) {
              const j = (i + 1) % poly.length;
              if (d[i] >= 0) next.push(poly[i]);
              if ((d[i] >= 0) !== (d[j] >= 0)) next.push(lerp(poly[i], poly[j], d[i] / (d[i] - d[j])));
            }
            poly = next;
            if (poly.length < 3) break;
          }
          for (let i = 1; i + 1 < poly.length; i++) { emit(poly[0]); emit(poly[i]); emit(poly[i + 1]); tris++; }
        }
        if (g.groups.length) ng.push({ start: before * 3, count: (tris - before) * 3, materialIndex: gr.materialIndex });
      }
      kept += tris;
      const geo = new THREE.BufferGeometry();
      names.forEach((name, k) => {
        const at = attrs[k], arr = new Float32Array(out[k]);
        if (name === "normal") for (let i = 0; i < arr.length; i += 3) { const l = Math.hypot(arr[i], arr[i + 1], arr[i + 2]) || 1; arr[i] /= l; arr[i + 1] /= l; arr[i + 2] /= l; }
        geo.setAttribute(name, new THREE.BufferAttribute(arr, at.itemSize, at.normalized));
      });
      for (const gr of ng) geo.addGroup(gr.start, gr.count, gr.materialIndex);
      swaps.push([o, o.geometry, geo]);
    });
    if (!kept) { for (const [, , geo] of swaps) geo.dispose(); throw new Error("nothing would be left: the box is empty"); }
    for (const [o, , geo] of swaps) o.geometry = geo;
    ground();
    return () => { for (const [o, old, geo] of swaps) { o.geometry = old; geo.dispose(); } ground(); };
  }
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
    show, look, reset, view, clear, stats, box,
    editing, shown, markers, matrix, setMatrix, apply, boxWith, center, clip, trim, parts,
    settle: () => tweenEnd(), // (a move going on: there at once)
    peek: (m) => { if (holder) { tweenEnd(); target = m.clone(); place(m); } }, // (the model there, as a slider drags: the ground left till setMatrix)
    setInset,
    set onPick(f) { onPick = f; canvas.style.cursor = f ? "crosshair" : ""; if (!f) hover.visible = false; },
    hoverColor, // (the hover ball's color: the step picking's)
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
