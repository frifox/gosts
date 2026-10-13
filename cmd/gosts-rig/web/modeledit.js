// Editing a model (the model viewer's Edit): leveling it on the ground,
// turning an edge onto the X or Y axis, scaling it to a known distance,
// and writing it out: an OBJ (and an STL), simplified if asked.
//
// In here, as in three.js, y is up and the ground is y = 0; the files are
// for Blender, Fusion 360 and the like, which have z up: an OBJ's y-up is
// turned to their z-up when they read it (so it's written as is), an STL is
// read as z-up already (so it's written turned: x, −z, y). X is x in both;
// their Y is −z here.
import * as THREE from "three";
import { mergeVertices } from "./vendor/BufferGeometryUtils.js";
import { MeshoptSimplifier } from "./vendor/meshopt_simplifier.module.js";

// UNITS: one unit of each, in meters.
export const UNITS = { mm: 0.001, in: 0.0254, m: 1 };

// recenter: the edit that puts box's middle over the origin (x, z; the
// height kept).
function recenter(box) {
  const c = box.getCenter(new THREE.Vector3());
  return new THREE.Matrix4().makeTranslation(-c.x, 0, -c.z);
}

// smallestAxis is the direction pts vary least along (the normal of the
// plane through them): of their covariance, the eigenvector with the
// smallest eigenvalue (power iteration on trace·I − C).
function smallestAxis(pts, c) {
  const C = [[0, 0, 0], [0, 0, 0], [0, 0, 0]];
  for (const p of pts) {
    const d = [p.x - c.x, p.y - c.y, p.z - c.z];
    for (let i = 0; i < 3; i++) for (let j = 0; j < 3; j++) C[i][j] += d[i] * d[j];
  }
  const t = C[0][0] + C[1][1] + C[2][2];
  const M = C.map((row, i) => row.map((v, j) => (i === j ? t : 0) - v));
  let v = new THREE.Vector3(0.3, 1, 0.2).normalize();
  for (let k = 0; k < 200; k++) {
    const w = new THREE.Vector3(
      M[0][0] * v.x + M[0][1] * v.y + M[0][2] * v.z,
      M[1][0] * v.x + M[1][1] * v.y + M[1][2] * v.z,
      M[2][0] * v.x + M[2][1] * v.y + M[2][2] * v.z);
    if (w.lengthSq() === 0) break;
    v = w.normalize();
  }
  return v;
}

// level: the edit that lays the plane through pts (3 or more, on what the
// model should stand on) on the ground, the model above it (center: the
// model's middle), then centers it. box(m): the model's box after m.
export function level(pts, center, box) {
  if (pts.length < 3) throw new Error("pick 3 or more points");
  const c = pts.reduce((a, p) => a.add(p), new THREE.Vector3()).divideScalar(pts.length);
  let n;
  if (pts.length === 3) n = new THREE.Vector3().subVectors(pts[1], pts[0]).cross(new THREE.Vector3().subVectors(pts[2], pts[0]));
  else n = smallestAxis(pts, c);
  const spread = Math.max(...pts.map((p) => p.distanceTo(c)));
  if (spread === 0 || (pts.length === 3 && n.length() < 1e-6 * spread * spread)) throw new Error("the points are in a line: pick points spread out");
  n.normalize();
  if (new THREE.Vector3().subVectors(center, c).dot(n) < 0) n.negate(); // (up: towards the model)
  const q = new THREE.Quaternion().setFromUnitVectors(n, new THREE.Vector3(0, 1, 0));
  // Turned about the points' middle, which then goes to the ground.
  const m = new THREE.Matrix4().makeTranslation(0, -c.y, 0)
    .multiply(new THREE.Matrix4().makeTranslation(c.x, c.y, c.z))
    .multiply(new THREE.Matrix4().makeRotationFromQuaternion(q))
    .multiply(new THREE.Matrix4().makeTranslation(-c.x, -c.y, -c.z));
  return recenter(box(m)).multiply(m);
}

// align: the edit that turns the model about the vertical so the line
// through pts (2 or more, along an edge; seen from above) runs along axis
// ("x" or "y": Blender's and Fusion's, so −z here), the least turn either
// way along it; then centers it.
export function align(pts, axis, box) {
  if (pts.length < 2) throw new Error("pick 2 or more points");
  let theta;
  if (pts.length === 2) theta = Math.atan2(pts[1].z - pts[0].z, pts[1].x - pts[0].x);
  else {
    const cx = pts.reduce((a, p) => a + p.x, 0) / pts.length, cz = pts.reduce((a, p) => a + p.z, 0) / pts.length;
    let sxx = 0, szz = 0, sxz = 0;
    for (const p of pts) { sxx += (p.x - cx) ** 2; szz += (p.z - cz) ** 2; sxz += (p.x - cx) * (p.z - cz); }
    theta = 0.5 * Math.atan2(2 * sxz, sxx - szz); // (the points' main direction)
  }
  const span = Math.max(...pts.map((p) => Math.hypot(p.x - pts[0].x, p.z - pts[0].z)));
  if (span < 1e-9) throw new Error("the points are one above the other: pick points along an edge");
  const target = axis === "y" ? -Math.PI / 2 : 0;
  // Turning by phi about y takes a direction's angle θ to θ − phi.
  let phi = (theta - target) % Math.PI;
  if (phi > Math.PI / 2) phi -= Math.PI;
  if (phi <= -Math.PI / 2) phi += Math.PI;
  const m = new THREE.Matrix4().makeRotationY(phi);
  return recenter(box(m)).multiply(m);
}

// rotate: the edit that turns the model deg (a multiple of 90) about axis
// ("x", "y", "z": Blender's and Fusion's; so x, −z, y here) through the
// origin, counterclockwise seen from the axis' end; nothing else moved (a
// leveled model turned about Z stays on its plane).
export function rotate(axis, deg) {
  const a = deg * Math.PI / 180;
  return axis === "x" ? new THREE.Matrix4().makeRotationX(a) : axis === "y" ? new THREE.Matrix4().makeRotationZ(-a) : new THREE.Matrix4().makeRotationY(a);
}

// center: the edit that slides the model along axes ("x", "y", "z", or
// some together, "xy" say: Blender's and Fusion's; their X, Y, Z: x, −z, y
// here) so point p is at 0 on each; the others not moved.
export function center(p, plane) {
  if (!p) throw new Error("pick a point");
  const ax = new Set(plane); // (x, y, z: Blender's)
  return new THREE.Matrix4().makeTranslation(ax.has("x") ? -p.x : 0, ax.has("z") ? -p.y : 0, ax.has("y") ? -p.z : 0);
}

// scale: the edit that makes the distance between a and b meters (about
// the origin: on the ground, centered, it stays so).
export function scale(a, b, meters) {
  const d = a.distanceTo(b);
  if (!(d > 0)) throw new Error("pick two different points");
  if (!(meters > 0)) throw new Error("the distance: a number over 0");
  const s = meters / d;
  return new THREE.Matrix4().makeScale(s, s, s);
}

// ---------------------------------------------------------------- saving
// fmt: n with up to d decimals, no trailing zeros.
function fmt(n, d) {
  const s = n.toFixed(d);
  return s.includes(".") ? s.replace(/\.?0+$/, "") || "0" : s;
}

// write turns the model's parts (modelView.parts(): non-indexed, meters)
// into the files: an OBJ (mtl: its materials' file name; the vertices
// welded, in units), simplified to about simplify triangles if that's set,
// and an STL too if stl. progress(text) as it goes. Its triangles, the OBJ
// and STL as Blobs.
export async function write(parts, { units = "mm", simplify = 0, stl = false, mtl = "", name = "model", progress = () => {} }) {
  const k = 1 / UNITS[units];
  const tick = () => new Promise((r) => setTimeout(r)); // (the page shown between steps)
  // Welded (shared corners shared: what simplifying needs, and a smaller
  // file), each part.
  progress("Welding the mesh");
  await tick();
  const meshes = parts.map((p) => {
    const g = mergeVertices(p.geometry, 1e-7);
    return { g, material: p.material, index: new Uint32Array(g.index.array) };
  });
  const total = meshes.reduce((a, m) => a + m.index.length / 3, 0);
  if (simplify > 0 && simplify < total) {
    progress(`Simplifying: ${total.toLocaleString()} triangles to about ${simplify.toLocaleString()}`);
    await tick();
    await MeshoptSimplifier.ready;
    for (const m of meshes) {
      const want = Math.max(1, Math.round(simplify * (m.index.length / 3) / total)) * 3;
      const pos = m.g.getAttribute("position").array;
      [m.index] = MeshoptSimplifier.simplify(m.index, pos instanceof Float32Array ? pos : new Float32Array(pos), 3, Math.min(want, m.index.length), 1);
    }
  }
  const triangles = meshes.reduce((a, m) => a + m.index.length / 3, 0);

  // The OBJ: y up, as three.js has it; each part's corners used, then its
  // faces (indices from 1, across the file).
  progress("Writing the OBJ");
  await tick();
  const chunks = [`# ${name}: made with gosts-rig, ${triangles} triangles, 1 unit = 1 ${units}, y up\n`];
  if (mtl) chunks.push(`mtllib ${mtl}\n`);
  let base = 0;
  for (const m of meshes) {
    const pos = m.g.getAttribute("position"), nor = m.g.getAttribute("normal"), uv = m.g.getAttribute("uv");
    const used = new Map(), order = [];
    for (const i of m.index) if (!used.has(i)) { used.set(i, order.length); order.push(i); }
    let out = [];
    const flush = () => { chunks.push(out.join("")); out = []; };
    for (const i of order) { out.push(`v ${fmt(pos.getX(i) * k, 5)} ${fmt(pos.getY(i) * k, 5)} ${fmt(pos.getZ(i) * k, 5)}\n`); if (out.length > 20000) flush(); }
    if (uv) for (const i of order) { out.push(`vt ${fmt(uv.getX(i), 6)} ${fmt(uv.getY(i), 6)}\n`); if (out.length > 20000) flush(); }
    if (nor) for (const i of order) { out.push(`vn ${fmt(nor.getX(i), 4)} ${fmt(nor.getY(i), 4)} ${fmt(nor.getZ(i), 4)}\n`); if (out.length > 20000) flush(); }
    if (m.material) out.push(`usemtl ${m.material}\n`);
    const corner = (i) => {
      const n = base + used.get(i) + 1;
      return uv && nor ? `${n}/${n}/${n}` : uv ? `${n}/${n}` : nor ? `${n}//${n}` : `${n}`;
    };
    for (let t = 0; t < m.index.length; t += 3) {
      out.push(`f ${corner(m.index[t])} ${corner(m.index[t + 1])} ${corner(m.index[t + 2])}\n`);
      if (out.length > 20000) flush();
    }
    flush();
    base += order.length;
  }
  const obj = new Blob(chunks, { type: "text/plain" });

  // The STL (binary): z up (x, −z, y), each triangle's normal from its
  // corners.
  let stlBlob = null;
  if (stl) {
    progress("Writing the STL");
    await tick();
    const buf = new ArrayBuffer(84 + triangles * 50), dv = new DataView(buf);
    const head = `${name}: gosts-rig, 1 unit = 1 ${units}, z up`.slice(0, 80);
    for (let i = 0; i < head.length; i++) dv.setUint8(i, head.charCodeAt(i) & 0x7f);
    dv.setUint32(80, triangles, true);
    let o = 84;
    const a = new THREE.Vector3(), b = new THREE.Vector3(), c = new THREE.Vector3(), n = new THREE.Vector3(), e = new THREE.Vector3();
    const zup = (v, i, pos) => v.set(pos.getX(i) * k, -pos.getZ(i) * k, pos.getY(i) * k);
    for (const m of meshes) {
      const pos = m.g.getAttribute("position");
      for (let t = 0; t < m.index.length; t += 3) {
        zup(a, m.index[t], pos); zup(b, m.index[t + 1], pos); zup(c, m.index[t + 2], pos);
        n.subVectors(b, a).cross(e.subVectors(c, a)).normalize();
        for (const v of [n, a, b, c]) { dv.setFloat32(o, v.x, true); dv.setFloat32(o + 4, v.y, true); dv.setFloat32(o + 8, v.z, true); o += 12; }
        o += 2; // (attribute bytes: 0)
      }
    }
    stlBlob = new Blob([buf], { type: "model/stl" });
  }
  for (const m of meshes) m.g.dispose();
  return { triangles, obj, stl: stlBlob };
}
