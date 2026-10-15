// Modify (the model editor's): picking out parts of the model (its
// triangles: an island, a lasso, a box, a brush) and deleting them, each
// hole that leaves filled if asked (flat, or smooth). Steps back and on
// (Undo, Redo) till applied; Cancel puts it as it was.
//
// The model's meshes are each a triangle soup (three.js' OBJ: no index,
// three corners a triangle); where triangles join is found by their
// corners' places (welded: the same place, the same vertex). A BVH
// (three-mesh-bvh, indirect: the triangles left in their order) finds what
// the pointer's over and what the brush covers.
import * as THREE from "three";
import { MeshBVH, INTERSECTED, NOT_INTERSECTED } from "./vendor/three-mesh-bvh.module.js";

// TOOLS: what a drag (or click) with the left button does.
export const TOOLS = ["lasso", "box", "brush", "island"];
export const FILLS = ["flat", "smooth"];

// --------------------------------------------------------------- the mesh

// weld: each corner's vertex (corners in the same place, within eps: one),
// and how many there are.
function weld(pos, eps) {
  const n = pos.count, inv = 1 / eps;
  const at = pos.isInterleavedBufferAttribute ? null : pos.array;
  let size = 1;
  while (size < n * 2) size <<= 1;
  const mask = size - 1, table = new Int32Array(size).fill(-1);
  const qx = new Int32Array(n), qy = new Int32Array(n), qz = new Int32Array(n), vid = new Int32Array(n);
  let V = 0;
  for (let i = 0; i < n; i++) {
    const x = Math.round((at ? at[i * 3] : pos.getX(i)) * inv), y = Math.round((at ? at[i * 3 + 1] : pos.getY(i)) * inv), z = Math.round((at ? at[i * 3 + 2] : pos.getZ(i)) * inv);
    qx[i] = x; qy[i] = y; qz[i] = z;
    let h = (Math.imul(x, 73856093) ^ Math.imul(y, 19349663) ^ Math.imul(z, 83492791)) & mask;
    for (;;) {
      const c = table[h];
      if (c < 0) { table[h] = i; vid[i] = V++; break; }
      if (qx[c] === x && qy[c] === y && qz[c] === z) { vid[i] = vid[c]; break; }
      h = (h + 1) & mask;
    }
  }
  return { vid, V };
}

// info: a geometry's triangles (T), corners' vertices (vid, V), each
// vertex's triangles (vtList[vtStart[v] … vtStart[v + 1]]) and its BVH
// (made when first wanted); kept with it.
const infos = new WeakMap();
function info(geo) {
  let f = infos.get(geo);
  if (f) return f;
  const pos = geo.getAttribute("position"), T = Math.floor(pos.count / 3);
  if (!geo.boundingBox) geo.computeBoundingBox();
  const eps = Math.max(geo.boundingBox.getSize(new THREE.Vector3()).length(), 1e-9) * 1e-7;
  const { vid, V } = weld(pos, eps);
  const vtStart = new Int32Array(V + 1), vtList = new Int32Array(T * 3);
  for (let i = 0; i < T * 3; i++) vtStart[vid[i] + 1]++;
  for (let v = 0; v < V; v++) vtStart[v + 1] += vtStart[v];
  const fill = vtStart.slice(0, V);
  for (let i = 0; i < T * 3; i++) vtList[fill[vid[i]]++] = (i / 3) | 0;
  f = { T, vid, V, vtStart, vtList, bvh: null };
  infos.set(geo, f);
  return f;
}
function bvhOf(geo) {
  const f = info(geo);
  return (f.bvh ||= new MeshBVH(geo, { indirect: true }));
}

// island: the triangles joined to t (by a vertex, on and on), as marked in
// sel (on: 1, off: 0).
function island(f, t, sel, on) {
  const { vid, vtStart, vtList } = f, seen = new Uint8Array(f.T), stack = [t];
  seen[t] = 1;
  while (stack.length) {
    const u = stack.pop();
    sel[u] = on;
    for (let k = 0; k < 3; k++) {
      const v = vid[u * 3 + k];
      for (let j = vtStart[v]; j < vtStart[v + 1]; j++) {
        const w = vtList[j];
        if (!seen[w]) { seen[w] = 1; stack.push(w); }
      }
    }
  }
}
// grow: sel and every triangle touching it; shrink: sel less those
// touching one that isn't.
function grow(f, sel, shrink = false) {
  const { vid, vtStart, vtList, T } = f, mark = new Uint8Array(f.V), out = sel.slice();
  for (let t = 0; t < T; t++) if (!!sel[t] !== shrink) for (let k = 0; k < 3; k++) mark[vid[t * 3 + k]] = 1;
  for (let v = 0; v < f.V; v++) {
    if (!mark[v]) continue;
    for (let j = vtStart[v]; j < vtStart[v + 1]; j++) out[vtList[j]] = shrink ? 0 : 1;
  }
  return out;
}

// holeLoops: the holes deleting sel would leave: loops of the kept
// triangles' edges that only a deleted one shared (the scan's own open
// edges left alone). Each a list of edges round the hole, the way a
// triangle filling it goes: { from, to } vertices, cf the corner (of the
// kept triangle) at from, t that triangle. Edges that don't close a loop
// (a hole run into an open edge): counted, as open.
function holeLoops(f, sel) {
  const { vid, vtStart, vtList, T } = f, gone = new Uint8Array(f.V), seen = new Uint8Array(T);
  for (let t = 0; t < T; t++) if (sel[t]) for (let k = 0; k < 3; k++) gone[vid[t * 3 + k]] = 1;
  const edges = [];
  const has = (u, b) => vid[u * 3] === b || vid[u * 3 + 1] === b || vid[u * 3 + 2] === b;
  for (let v = 0; v < f.V; v++) {
    if (!gone[v]) continue;
    for (let j = vtStart[v]; j < vtStart[v + 1]; j++) {
      const t = vtList[j];
      if (sel[t] || seen[t]) continue;
      seen[t] = 1;
      for (let k = 0; k < 3; k++) {
        const ca = t * 3 + k, cb = t * 3 + (k + 1) % 3, a = vid[ca], b = vid[cb];
        if (a === b || !gone[a] || !gone[b]) continue;
        let kept = 0, del = 0;
        for (let i = vtStart[a]; i < vtStart[a + 1]; i++) {
          const u = vtList[i];
          if (i > vtStart[a] && vtList[i - 1] === u) continue; // (a triangle with a twice: once)
          if (has(u, b)) sel[u] ? del++ : kept++;
        }
        if (kept === 1 && del > 0) edges.push({ from: b, to: a, cf: cb, t }); // (round the hole: b to a)
      }
    }
  }
  const out = new Map();
  edges.forEach((e, i) => { const l = out.get(e.from); l ? l.push(i) : out.set(e.from, [i]); });
  const used = new Uint8Array(edges.length), loops = [];
  let open = 0;
  for (let i = 0; i < edges.length; i++) {
    if (used[i]) continue;
    used[i] = 1;
    const loop = [edges[i]];
    let at = edges[i].to, closed = false;
    while (loop.length <= edges.length) {
      if (at === edges[i].from) { closed = true; break; }
      const next = out.get(at)?.find((k) => !used[k]);
      if (next === undefined) break;
      used[next] = 1;
      loop.push(edges[next]);
      at = edges[next].to;
    }
    if (closed && loop.length >= 3) loops.push(loop);
    else open++;
  }
  return { loops, open };
}

// fillLoop: triangles over a hole (P: its points in order round it, the
// way a triangle filling it goes): flat, the loop's own (on the plane it
// most lies in: earcut); smooth, those made finer (about as fine as the
// mesh round it: Liepa's centroid splits, edges flipped to keep them
// shapely) and the inside eased into a smooth skin. Its points (the loop's
// first, then any inside) and triangles (corner indices).
function fillLoop(P0, mode, maxTris) {
  const n = P0.length;
  // The plane: the loop's Newell normal; u × v = N, so the loop goes round
  // it counterclockwise.
  const N = new THREE.Vector3();
  for (let i = 0; i < n; i++) {
    const a = P0[i], b = P0[(i + 1) % n];
    N.x += (a.y - b.y) * (a.z + b.z); N.y += (a.z - b.z) * (a.x + b.x); N.z += (a.x - b.x) * (a.y + b.y);
  }
  if (N.lengthSq() < 1e-30) return null;
  N.normalize();
  const u = Math.abs(N.x) < 0.9 ? new THREE.Vector3(1, 0, 0) : new THREE.Vector3(0, 1, 0);
  u.sub(N.clone().multiplyScalar(u.dot(N))).normalize();
  const v = new THREE.Vector3().crossVectors(N, u);
  const flat = P0.map((p) => new THREE.Vector2(p.dot(u), p.dot(v)));
  let tris = n === 3 ? [[0, 1, 2]] : THREE.ShapeUtils.triangulateShape(flat.slice(), []).map((t) => [...t]);
  for (const t of tris) {
    const [a, b, c] = t.map((i) => flat[i]);
    if ((b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x) < 0) [t[1], t[2]] = [t[2], t[1]];
  }
  let P = P0.map((p) => p.clone());
  if (mode === "smooth" && n > 3) ({ P, tris } = refine(P, tris, maxTris));
  return { P, tris };
}
const ekey = (a, b) => (a < b ? a * 1048576 + b : b * 1048576 + a); // (a patch: fewer than 2²⁰ points)
function refine(P, tris, maxTris) {
  const n = P.length, sig = [];
  for (let i = 0; i < n; i++) sig.push((P[i].distanceTo(P[(i + 1) % n]) + P[i].distanceTo(P[(i + n - 1) % n])) / 2);
  // Too big a hole for its edges' size: coarser (at most maxTris).
  let area = 0;
  for (const [a, b, c] of tris) area += new THREE.Vector3().subVectors(P[b], P[a]).cross(new THREE.Vector3().subVectors(P[c], P[a])).length() / 2;
  const mean = sig.reduce((s, x) => s + x, 0) / n, want = area / (mean * mean * 0.433);
  if (want > maxTris) { const k = Math.sqrt(want / maxTris); for (let i = 0; i < n; i++) sig[i] *= k; }
  const rim = new Set();
  for (let i = 0; i < n; i++) rim.add(ekey(i, (i + 1) % n));
  const A = Math.SQRT2, m = new THREE.Vector3();
  for (let pass = 0; pass < 40; pass++) {
    let split = false;
    const next = [];
    for (const [a, b, c] of tris) {
      m.copy(P[a]).add(P[b]).add(P[c]).divideScalar(3);
      const sm = (sig[a] + sig[b] + sig[c]) / 3;
      if (next.length + 3 < maxTris && [a, b, c].every((k) => { const d = A * m.distanceTo(P[k]); return d > sm && d > sig[k]; })) {
        const k = P.push(m.clone()) - 1;
        sig.push(sm);
        next.push([a, b, k], [b, c, k], [c, a, k]);
        split = true;
      } else next.push([a, b, c]);
    }
    tris = next;
    relax(P, tris, rim);
    if (!split) break;
  }
  fair(P, tris, n);
  return { P, tris };
}
// relax: inside edges flipped where the two triangles' far corners' angles
// add up to more than 180° (as a Delaunay mesh's don't), till none are.
function relax(P, tris, rim) {
  const angle = (x, a, b) => new THREE.Vector3().subVectors(P[a], P[x]).angleTo(new THREE.Vector3().subVectors(P[b], P[x]));
  const normal = (a, b, c) => new THREE.Vector3().subVectors(P[b], P[a]).cross(new THREE.Vector3().subVectors(P[c], P[a]));
  for (let round = 0; round < 12; round++) {
    const edges = new Map();
    tris.forEach((t, i) => { for (let k = 0; k < 3; k++) { const a = t[k], b = t[(k + 1) % 3], key = ekey(a, b); const l = edges.get(key); const e = { i, a, b, x: t[(k + 2) % 3] }; l ? l.push(e) : edges.set(key, [e]); } });
    const busy = new Uint8Array(tris.length);
    let flips = 0;
    for (const [key, l] of edges) {
      if (l.length !== 2 || rim.has(key)) continue;
      const [e1, e2] = l;
      if (busy[e1.i] || busy[e2.i] || e1.x === e2.x || e2.a !== e1.b || edges.has(ekey(e1.x, e2.x))) continue;
      if (angle(e1.x, e1.a, e1.b) + angle(e2.x, e2.a, e2.b) <= Math.PI + 1e-9) continue;
      const u = e1.a, v = e1.b, x = e1.x, y = e2.x, was = normal(u, v, x).add(normal(v, u, y));
      if (normal(x, u, y).dot(was) <= 0 || normal(y, v, x).dot(was) <= 0) continue; // (not convex: it'd fold)
      tris[e1.i] = [x, u, y];
      tris[e2.i] = [y, v, x];
      busy[e1.i] = busy[e2.i] = 1;
      flips++;
    }
    if (!flips) break;
  }
}
// fair: the inside points (from fixed on) eased to their neighbors'
// middle, over and over: a smooth skin from the loop.
function fair(P, tris, fixed) {
  if (P.length <= fixed) return;
  const nb = Array.from({ length: P.length }, () => new Set());
  for (const [a, b, c] of tris) { nb[a].add(b).add(c); nb[b].add(a).add(c); nb[c].add(a).add(b); }
  const m = new THREE.Vector3();
  for (let it = 0; it < 80; it++) {
    for (let i = fixed; i < P.length; i++) {
      if (!nb[i].size) continue;
      m.set(0, 0, 0);
      for (const j of nb[i]) m.add(P[j]);
      P[i].lerp(m.divideScalar(nb[i].size), 0.6);
    }
  }
}

// remove: geo less the triangles in sel (each material's kept together),
// the holes filled if asked (fill: flat or smooth; each fill triangle has
// the texture's spot at the hole's nearest edge point, the material of
// the triangle next to it). The new geometry, and how it went.
function remove(geo, sel, fill) {
  const f = info(geo), T = f.T;
  const names = Object.keys(geo.attributes), attrs = names.map((n) => geo.getAttribute(n));
  const groups = geo.groups.length ? geo.groups : [{ start: 0, count: T * 3, materialIndex: 0 }];
  const groupOf = (t) => Math.max(0, groups.findIndex((g) => t * 3 >= g.start && t * 3 < g.start + g.count));
  const patches = groups.map(() => []);
  let holes = 0, open = 0;
  if (fill) {
    const r = holeLoops(f, sel);
    open = r.open;
    const pos = geo.getAttribute("position");
    for (const loop of r.loops) {
      const P = loop.map((e) => new THREE.Vector3().fromBufferAttribute(pos, e.cf));
      const p = fillLoop(P, fill, 20000);
      if (!p || !p.tris.length) { open++; continue; }
      // Each point's nearest loop corner (the loop's own: itself).
      const near = p.P.map((q, i) => {
        if (i < P.length) return loop[i].cf;
        let best = 0, bd = Infinity;
        for (let k = 0; k < P.length; k++) { const d = q.distanceToSquared(P[k]); if (d < bd) { bd = d; best = k; } }
        return loop[best].cf;
      });
      patches[groupOf(loop[0].t)].push({ P: p.P, tris: p.tris, near });
      holes++;
    }
  }
  let kept = 0, added = 0;
  for (let t = 0; t < T; t++) if (!sel[t]) kept++;
  for (const l of patches) for (const p of l) added += p.tris.length;
  const outs = attrs.map((a) => new Float32Array((kept + added) * 3 * a.itemSize));
  const ng = [];
  let o = 0; // (corners written)
  const nrm = new THREE.Vector3(), e1 = new THREE.Vector3(), e2 = new THREE.Vector3();
  groups.forEach((g, gi) => {
    const start = o;
    for (let t = g.start / 3; t < Math.min(T, (g.start + g.count) / 3); t++) {
      if (sel[t]) continue;
      attrs.forEach((a, k) => { const s = a.itemSize; for (let c = 0; c < 3; c++) for (let j = 0; j < s; j++) outs[k][(o + c) * s + j] = a.getComponent(t * 3 + c, j); });
      o += 3;
    }
    for (const p of patches[gi]) {
      for (const tri of p.tris) {
        e1.subVectors(p.P[tri[1]], p.P[tri[0]]); e2.subVectors(p.P[tri[2]], p.P[tri[0]]);
        nrm.crossVectors(e1, e2).normalize();
        const src = p.near[tri[0]]; // (its texture: one spot, the whole triangle)
        for (let c = 0; c < 3; c++) {
          const q = p.P[tri[c]];
          attrs.forEach((a, k) => {
            const s = a.itemSize, at = (o + c) * s;
            if (names[k] === "position") { outs[k][at] = q.x; outs[k][at + 1] = q.y; outs[k][at + 2] = q.z; }
            else if (names[k] === "normal") { outs[k][at] = nrm.x; outs[k][at + 1] = nrm.y; outs[k][at + 2] = nrm.z; }
            else for (let j = 0; j < s; j++) outs[k][at + j] = a.getComponent(src, j);
          });
        }
        o += 3;
      }
    }
    if (geo.groups.length) ng.push({ start, count: o - start, materialIndex: g.materialIndex });
  });
  const out = new THREE.BufferGeometry();
  names.forEach((n, k) => out.setAttribute(n, new THREE.BufferAttribute(outs[k], attrs[k].itemSize, attrs[k].normalized)));
  for (const g of ng) out.addGroup(g.start, g.count, g.materialIndex);
  return { geo: out, deleted: T - kept, holes, open, added };
}

// ------------------------------------------------------------ the editor

const SEL_MAT = new THREE.MeshBasicMaterial({ color: 0xff7a1a, transparent: true, opacity: 0.5, side: THREE.DoubleSide, depthWrite: false,
  polygonOffset: true, polygonOffsetFactor: -2, polygonOffsetUnits: -4 });
// DEPTH: each pixel's distance from the camera (view space; 0: nothing),
// to tell what's seen from what's behind it.
const DEPTH = new THREE.ShaderMaterial({
  side: THREE.DoubleSide,
  clipping: true,
  vertexShader: `#include <clipping_planes_pars_vertex>
varying float vDepth;
void main() {
  vec4 mvPosition = modelViewMatrix * vec4(position, 1.0);
  vDepth = -mvPosition.z;
  gl_Position = projectionMatrix * mvPosition;
#include <clipping_planes_vertex>
}`,
  fragmentShader: `#include <clipping_planes_pars_fragment>
varying float vDepth;
void main() {
#include <clipping_planes_fragment>
  gl_FragColor = vec4(vDepth, 0.0, 0.0, 1.0);
}`,
});

// createModify: Modify for the viewer's model. v: what it needs of the
// viewer: canvas, camera, renderer, scene, controls, planes (the trim's
// clipping), obj() (the model), radius() (its size), changed() (its
// triangles changed: the ground again). onChange: what's selected, or the
// steps, changed (the page's panel).
export function createModify(v) {
  const { canvas, camera, renderer, scene, controls, planes } = v;
  SEL_MAT.clippingPlanes = DEPTH.clippingPlanes = planes;
  const group = new THREE.Group(); // (the selections shown: each over its mesh)
  group.renderOrder = 3;
  scene.add(group);
  // The drawing over the canvas: the lasso, the box, the brush.
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("style", "position:absolute;inset:0;width:100%;height:100%;pointer-events:none;overflow:visible");
  const shape = document.createElementNS(NS, "path"), ring = document.createElementNS(NS, "circle");
  for (const e of [shape, ring]) { e.setAttribute("fill", "rgba(255,122,26,.12)"); e.setAttribute("stroke", "#ff7a1a"); e.setAttribute("stroke-width", "1.5"); e.setAttribute("vector-effect", "non-scaling-stroke"); }
  shape.setAttribute("stroke-dasharray", "5 4");
  ring.setAttribute("fill", "rgba(255,122,26,.08)");
  svg.append(shape, ring);
  canvas.parentElement.insertBefore(svg, canvas.nextSibling);
  shape.style.display = ring.style.display = "none";

  let tool = null, brush = 24, onChange = () => {}, shownOn = true;
  // hist: the steps, each { geos, sels } (each mesh's geometry and its
  // triangles selected); at: the one now. null: nothing done yet.
  let hist = null, at = 0;
  const overlays = new Map(); // mesh → its selection, shown
  const meshes = () => { const l = []; v.obj()?.traverse((o) => { if (o.isMesh) l.push(o); }); return l; };
  const cur = () => hist?.[at];

  // begin: the first step (the model as it is: nothing selected).
  function begin() {
    if (hist) return;
    const geos = new Map(), sels = new Map();
    for (const o of meshes()) {
      if (o.geometry.index) o.geometry = o.geometry.toNonIndexed(); // (a soup, as an OBJ's)
      geos.set(o, o.geometry);
      sels.set(o, new Uint8Array(info(o.geometry).T));
    }
    hist = [{ geos, sels }];
    at = 0;
  }
  const geosIn = (states) => { const s = new Set(); for (const st of states) for (const g of st.geos.values()) s.add(g); return s; };
  // push: a step on (the ones undone before it: gone, their geometries freed).
  function push(st) {
    const dropped = hist.splice(at + 1);
    const keep = geosIn(hist), was = geosIn([st]);
    for (const g of geosIn(dropped)) if (!keep.has(g) && !was.has(g)) g.dispose();
    hist.push(st);
    at = hist.length - 1;
    show();
  }
  // show: the step now on the model: its geometries, its selections.
  function show() {
    const st = cur();
    let geoChanged = false;
    for (const o of meshes()) {
      const g = st?.geos.get(o);
      if (g && o.geometry !== g) { o.geometry = g; geoChanged = true; }
      overlay(o, st?.sels.get(o));
    }
    if (geoChanged) v.changed();
    onChange();
  }
  function overlay(o, sel) {
    let ov = overlays.get(o);
    if (!sel) { if (ov) ov.visible = false; return; }
    const geo = o.geometry, T = info(geo).T;
    let n = 0;
    for (let t = 0; t < T; t++) n += sel[t];
    if (!ov) {
      ov = new THREE.Mesh(new THREE.BufferGeometry(), SEL_MAT);
      ov.matrixAutoUpdate = false;
      ov.raycast = () => {};
      group.add(ov);
      overlays.set(o, ov);
    }
    ov.visible = n > 0;
    if (!n) return;
    if (ov.userData.src !== geo) { // (its own index, as big as the mesh's triangles; the mesh's points)
      ov.geometry.deleteAttribute("position"); // (shared: not freed with it)
      ov.geometry.dispose();
      ov.geometry = new THREE.BufferGeometry();
      ov.geometry.setAttribute("position", geo.getAttribute("position"));
      ov.geometry.setIndex(new THREE.BufferAttribute(new Uint32Array(T * 3), 1));
      ov.userData.src = geo;
    }
    const idx = ov.geometry.index, a = idx.array;
    let k = 0;
    for (let t = 0; t < T; t++) if (sel[t]) { a[k++] = t * 3; a[k++] = t * 3 + 1; a[k++] = t * 3 + 2; }
    idx.clearUpdateRanges();
    idx.addUpdateRange(0, k);
    idx.needsUpdate = true;
    ov.geometry.setDrawRange(0, k);
  }
  // tick: the selections where their meshes are (each frame).
  function tick() {
    group.visible = shownOn;
    for (const [o, ov] of overlays) { ov.matrix.copy(o.matrixWorld); ov.matrixWorld.copy(o.matrixWorld); }
  }

  // ---------------------------------------------------------- selecting

  // depth: what the camera sees (each pixel's distance), for "seen" below.
  let rt = null;
  function depth() {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (!rt) rt = new THREE.WebGLRenderTarget(w, h, { type: THREE.FloatType });
    rt.setSize(w, h);
    const hidden = [];
    for (const c of scene.children) if (c.visible && c !== camera && !c.getObjectById(v.obj().id)) { c.visible = false; hidden.push(c); }
    const cc = renderer.getClearColor(new THREE.Color()), ca = renderer.getClearAlpha(), was = renderer.getRenderTarget();
    scene.overrideMaterial = DEPTH;
    renderer.setRenderTarget(rt);
    renderer.setClearColor(0x000000, 0);
    renderer.clear();
    renderer.render(scene, camera);
    renderer.setRenderTarget(was);
    renderer.setClearColor(cc, ca);
    scene.overrideMaterial = null;
    for (const c of hidden) c.visible = true;
    const buf = new Float32Array(w * h * 4);
    renderer.readRenderTargetPixels(rt, 0, 0, w, h, buf);
    return { w, h, buf, tol: v.radius() * 0.01 };
  }
  // seen: a point at sx, sy (css px) z from the camera isn't behind what's
  // drawn there (nothing drawn: a sliver, seen).
  function seen(d, sx, sy, z) {
    const x = sx | 0, y = d.h - 1 - (sy | 0);
    if (x < 0 || y < 0 || x >= d.w || y >= d.h) return false;
    const b = d.buf[(y * d.w + x) * 4];
    return b === 0 || z <= b + d.tol;
  }
  // centers: each triangle's middle on the screen (css px) and how far off.
  function centers(o, fn) {
    const geo = o.geometry, pos = geo.getAttribute("position"), T = info(geo).T, a = pos.array;
    const W = canvas.clientWidth, H = canvas.clientHeight;
    const e = new THREE.Matrix4().multiplyMatrices(camera.projectionMatrix, camera.matrixWorldInverse).multiply(o.matrixWorld).elements;
    for (let t = 0; t < T; t++) {
      const i = t * 9, x = (a[i] + a[i + 3] + a[i + 6]) / 3, y = (a[i + 1] + a[i + 4] + a[i + 7]) / 3, z = (a[i + 2] + a[i + 5] + a[i + 8]) / 3;
      const w = e[3] * x + e[7] * y + e[11] * z + e[15];
      if (w <= 0) continue;
      const nx = (e[0] * x + e[4] * y + e[8] * z + e[12]) / w, ny = (e[1] * x + e[5] * y + e[9] * z + e[13]) / w;
      fn(t, (nx + 1) / 2 * W, (1 - ny) / 2 * H, w);
    }
  }
  // hitAt: the triangle under client x, y: { o, t, point (as seen), dist }.
  const ray = new THREE.Raycaster();
  function hitAt(x, y) {
    const b = canvas.getBoundingClientRect();
    ray.setFromCamera(new THREE.Vector2((x - b.left) / b.width * 2 - 1, -((y - b.top) / b.height) * 2 + 1), camera);
    let best = null;
    for (const o of meshes()) {
      const inv = o.matrixWorld.clone().invert(), r = ray.ray.clone().applyMatrix4(inv);
      const h = bvhOf(o.geometry).raycastFirst(r, THREE.DoubleSide);
      if (!h) continue;
      const p = h.point.clone().applyMatrix4(o.matrixWorld), dist = p.distanceTo(ray.ray.origin);
      if (!best || dist < best.dist) best = { o, t: h.faceIndex, point: p, dist };
    }
    return best;
  }
  // sels: a copy of the selections now, to change (a step's).
  const copySels = () => new Map([...cur().sels].map(([o, s]) => [o, s.slice()]));
  const local = (x, y) => { const b = canvas.getBoundingClientRect(); return [x - b.left, y - b.top]; };

  // A drag (or click) with the left button, a tool picked: what it does.
  // mode: "set" (the selection: this), "add" (⇧), "sub" (⌥); the brush
  // adds unless ⌥.
  let drag = null;
  canvas.addEventListener("pointerdown", (e) => {
    if (!tool || e.button !== 0 || !v.obj()) return;
    begin();
    canvas.setPointerCapture(e.pointerId);
    const mode = e.altKey ? "sub" : e.shiftKey || tool === "brush" ? "add" : "set";
    drag = { mode, pts: [local(e.clientX, e.clientY)], sels: copySels(), moved: false };
    if (tool === "brush") { drag.d = depth(); paint(e.clientX, e.clientY); }
  });
  canvas.addEventListener("pointermove", (e) => {
    if (tool === "brush") ringAt(e);
    if (!drag) return;
    const p = local(e.clientX, e.clientY), last = drag.pts[drag.pts.length - 1];
    if (Math.hypot(p[0] - drag.pts[0][0], p[1] - drag.pts[0][1]) > 3) drag.moved = true;
    if (tool === "lasso") { if (Math.hypot(p[0] - last[0], p[1] - last[1]) >= 2) drag.pts.push(p); outline(); }
    else if (tool === "box") { drag.pts[1] = p; outline(); }
    else if (tool === "brush") {
      // Along the way from the last spot (no gaps, moved fast).
      const steps = Math.max(1, Math.ceil(Math.hypot(p[0] - last[0], p[1] - last[1]) / (brush / 2)));
      const b = canvas.getBoundingClientRect();
      for (let i = 1; i <= steps; i++) paint(b.left + last[0] + (p[0] - last[0]) * i / steps, b.top + last[1] + (p[1] - last[1]) * i / steps);
      drag.pts.push(p);
    }
  });
  const end = (e) => {
    if (!drag) return;
    const d = drag;
    drag = null;
    shape.style.display = "none";
    if (e.type === "pointercancel") { show(); return; }
    if (tool === "island") { if (!d.moved) islandAt(e.clientX, e.clientY, d); else show(); return; }
    if (tool === "lasso" || tool === "box") return region(d);
    if (tool === "brush") push({ geos: cur().geos, sels: d.sels });
  };
  canvas.addEventListener("pointerup", end);
  canvas.addEventListener("pointercancel", end);
  canvas.addEventListener("pointerleave", () => { ring.style.display = "none"; });

  function outline() {
    const p = drag.pts;
    shape.style.display = "";
    if (tool === "box" && p[1]) shape.setAttribute("d", `M${p[0][0]},${p[0][1]}H${p[1][0]}V${p[1][1]}H${p[0][0]}Z`);
    else shape.setAttribute("d", "M" + p.map((q) => q.join(",")).join("L") + "Z");
  }
  function ringAt(e) {
    const [x, y] = local(e.clientX, e.clientY);
    ring.setAttribute("cx", x); ring.setAttribute("cy", y); ring.setAttribute("r", brush);
    ring.style.display = "";
  }
  function islandAt(x, y, d) {
    const h = hitAt(x, y);
    if (d.mode === "set") for (const s of d.sels.values()) s.fill(0);
    if (h) island(info(h.o.geometry), h.t, d.sels.get(h.o), d.mode === "sub" ? 0 : 1);
    if (!h && d.mode !== "set") return;
    push({ geos: cur().geos, sels: d.sels });
  }
  // region: the triangles whose middles are in the lasso (or box) and seen.
  function region(d) {
    const W = canvas.clientWidth, H = canvas.clientHeight;
    let inside;
    if (tool === "box") {
      if (!d.pts[1]) return show();
      const [x0, x1] = [d.pts[0][0], d.pts[1][0]].sort((a, b) => a - b), [y0, y1] = [d.pts[0][1], d.pts[1][1]].sort((a, b) => a - b);
      inside = (x, y) => x >= x0 && x <= x1 && y >= y0 && y <= y1;
    } else {
      if (d.pts.length < 3) return show();
      const cv = document.createElement("canvas");
      cv.width = W; cv.height = H;
      const cx = cv.getContext("2d", { willReadFrequently: true });
      cx.beginPath();
      d.pts.forEach(([x, y], i) => (i ? cx.lineTo(x, y) : cx.moveTo(x, y)));
      cx.closePath();
      cx.fill();
      const m = cx.getImageData(0, 0, W, H).data;
      inside = (x, y) => { const i = ((y | 0) * W + (x | 0)) * 4 + 3; return x >= 0 && y >= 0 && x < W && y < H && m[i] > 127; };
    }
    const dp = depth(), on = d.mode === "sub" ? 0 : 1;
    for (const [o, s] of d.sels) {
      if (d.mode === "set") s.fill(0);
      centers(o, (t, x, y, z) => { if (inside(x, y) && seen(dp, x, y, z)) s[t] = on; });
    }
    push({ geos: cur().geos, sels: d.sels });
  }
  // paint: the brush at client x, y: the triangles (seen) with their
  // middles within its circle, on the surface under it.
  function paint(x, y) {
    const h = hitAt(x, y);
    if (!h) return;
    const H = canvas.clientHeight, depthZ = h.point.clone().applyMatrix4(camera.matrixWorldInverse).z * -1;
    const r = brush * 2 * depthZ * Math.tan(THREE.MathUtils.degToRad(camera.fov) / 2) / H / (camera.zoom || 1);
    const on = drag.mode === "sub" ? 0 : 1;
    for (const o of meshes()) {
      const inv = o.matrixWorld.clone().invert(), k = 1 / o.matrixWorld.getMaxScaleOnAxis();
      const sphere = new THREE.Sphere(h.point.clone().applyMatrix4(inv), r * k);
      const s = drag.sels.get(o), W = canvas.clientWidth;
      const e = new THREE.Matrix4().multiplyMatrices(camera.projectionMatrix, camera.matrixWorldInverse).multiply(o.matrixWorld).elements;
      const c = new THREE.Vector3();
      bvhOf(o.geometry).shapecast({
        intersectsBounds: (box) => (sphere.intersectsBox(box) ? INTERSECTED : NOT_INTERSECTED),
        intersectsTriangle: (tri, t) => {
          if (s[t] === on) return false;
          c.copy(tri.a).add(tri.b).add(tri.c).divideScalar(3);
          if (c.distanceTo(sphere.center) > sphere.radius) return false;
          const w = e[3] * c.x + e[7] * c.y + e[11] * c.z + e[15];
          if (w <= 0) return false;
          const sx = ((e[0] * c.x + e[4] * c.y + e[8] * c.z + e[12]) / w + 1) / 2 * W, sy = (1 - (e[1] * c.x + e[5] * c.y + e[9] * c.z + e[13]) / w) / 2 * H;
          if (seen(drag.d, sx, sy, w)) s[t] = on;
          return false;
        },
      });
      overlay(o, s);
    }
  }

  // ------------------------------------------------------------ the API

  function setTool(t) {
    tool = TOOLS.includes(t) ? t : null;
    controls.mouseButtons.LEFT = tool ? -1 : THREE.MOUSE.PAN; // (a drag: the tool's, not moving the view)
    canvas.style.cursor = tool === "brush" ? "none" : tool ? "crosshair" : "";
    if (tool !== "brush") ring.style.display = "none";
    if (!tool && drag) { drag = null; shape.style.display = "none"; show(); }
    onChange();
  }
  const count = () => { let n = 0; if (cur()) for (const s of cur().sels.values()) for (let t = 0; t < s.length; t++) n += s[t]; return n; };
  // op: Grow, Shrink, Invert, Clear: a step.
  function op(name) {
    if (!hist) return;
    const sels = new Map();
    for (const [o, s] of cur().sels) {
      const f = info(cur().geos.get(o));
      sels.set(o, name === "grow" ? grow(f, s) : name === "shrink" ? grow(f, s, true) : name === "invert" ? s.map((x) => 1 - x) : new Uint8Array(s.length));
    }
    push({ geos: cur().geos, sels });
  }
  // del: the selected triangles deleted (the holes filled: fill, flat or
  // smooth; null: left open). How it went: { deleted, holes, open }.
  function del(fill) {
    if (!hist || !count()) return null;
    const geos = new Map(cur().geos), sels = new Map(), r = { deleted: 0, holes: 0, open: 0 };
    for (const [o, s] of cur().sels) {
      const g = cur().geos.get(o);
      if (!s.some((x) => x)) { sels.set(o, s.slice()); continue; }
      const d = remove(g, s, fill);
      geos.set(o, d.geo);
      sels.set(o, new Uint8Array(info(d.geo).T));
      r.deleted += d.deleted; r.holes += d.holes; r.open += d.open;
    }
    push({ geos, sels });
    return r;
  }
  // changed: any triangles deleted, at the step now (to Apply).
  const changed = () => !!hist && [...cur().geos].some(([o, g]) => hist[0].geos.get(o) !== g);
  // commit (Apply): the deletes kept, as one edit: a function that puts
  // the model back as it was before them (or null: none); the steps gone.
  function commit() {
    if (!hist) return null;
    const first = hist[0].geos, now = cur().geos, did = changed();
    const keep = new Set([...first.values(), ...now.values()]);
    for (const g of geosIn(hist)) if (!keep.has(g)) g.dispose();
    hist = null;
    show();
    if (!did) return null;
    return () => { for (const [o, g] of first) { const was = o.geometry; o.geometry = g; if (was !== g) was.dispose(); } v.changed(); };
  }
  // cancel: as it was before the first step (nothing selected).
  function cancel() {
    if (!hist) return;
    at = 0;
    show();
    for (const g of geosIn(hist.splice(1))) if (!geosIn([hist[0]]).has(g)) g.dispose();
    hist = null;
    show();
  }
  // reset: the steps forgotten (the model as it is: a new one, or cut).
  function reset() {
    hist = null;
    drag = null;
    for (const ov of overlays.values()) { ov.geometry.deleteAttribute("position"); ov.geometry.dispose(); group.remove(ov); }
    overlays.clear();
    onChange();
  }
  return {
    tick, op, del, commit, cancel, reset, changed, count,
    get tool() { return tool; }, set tool(t) { setTool(t); },
    get brush() { return brush; },
    brushBy(k) { brush = Math.max(4, Math.min(300, Math.round(brush * k))); if (ring.style.display !== "none") ring.setAttribute("r", brush); onChange(); },
    undo() { if (hist && at > 0) { at--; show(); } },
    redo() { if (hist && at < hist.length - 1) { at++; show(); } },
    get canUndo() { return !!hist && at > 0; },
    get canRedo() { return !!hist && at < hist.length - 1; },
    shown(on) { shownOn = on; if (!on) setTool(null); },
    set onChange(f) { onChange = f; },
  };
}
