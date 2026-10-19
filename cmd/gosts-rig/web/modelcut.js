// Cut (the model editor's): the model sliced by a plane (XY, XZ or YZ),
// either side of it taken away (Cut), or the face it's on made flat on it
// (Flatten: a scanned floor that's dished, bulging or rounded off, say).
//
// The plane is in the world (as the model's shown, edited): a three.js
// Plane. Cut: its normal points to the side taken away (beyond: distance >
// 0); each mesh's triangles are split where they cross it, the pieces
// beyond go, and each opening left on the plane is closed, flat, its holes
// (a ring's middle) kept. Flatten: its normal points out of the face (a
// floor: down); that face (the surface seen from that side, facing it,
// joined to where the plane is) has each of its points put on the plane:
// hills pulled back, dips pulled out, the sides round it stretched.
import * as THREE from "three";
import { MeshBVH } from "./vendor/three-mesh-bvh.module.js";

// sectionSegments: where the model crosses plane: line segments (world),
// two points each, as an array (x, y, z, x, y, z, …).
export function sectionSegments(obj, plane) {
  const out = [], n = plane.normal, c = plane.constant;
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const pos = o.geometry.getAttribute("position"), a = pos.array, s = pos.itemSize, e = o.matrixWorld.elements;
    const idx = o.geometry.index, T = idx ? idx.count / 3 : pos.count / 3;
    const w = new Float64Array(9), d = [0, 0, 0];
    for (let t = 0; t < T; t++) {
      for (let k = 0; k < 3; k++) {
        const i = (idx ? idx.getX(t * 3 + k) : t * 3 + k) * s, x = a[i], y = a[i + 1], z = a[i + 2];
        const wx = e[0] * x + e[4] * y + e[8] * z + e[12], wy = e[1] * x + e[5] * y + e[9] * z + e[13], wz = e[2] * x + e[6] * y + e[10] * z + e[14];
        w[k * 3] = wx; w[k * 3 + 1] = wy; w[k * 3 + 2] = wz;
        d[k] = n.x * wx + n.y * wy + n.z * wz + c;
      }
      if ((d[0] > 0 && d[1] > 0 && d[2] > 0) || (d[0] < 0 && d[1] < 0 && d[2] < 0)) continue;
      let got = 0;
      for (let k = 0; k < 3 && got < 2; k++) {
        const j = (k + 1) % 3;
        if ((d[k] < 0 && d[j] >= 0) || (d[k] >= 0 && d[j] < 0)) {
          const f = d[k] / (d[k] - d[j]);
          out.push(w[k * 3] + (w[j * 3] - w[k * 3]) * f, w[k * 3 + 1] + (w[j * 3 + 1] - w[k * 3 + 1]) * f, w[k * 3 + 2] + (w[j * 3 + 2] - w[k * 3 + 2]) * f);
          got++;
        }
      }
      if (got === 1) out.length -= 3; // (a corner just touching it)
    }
  });
  return out;
}

// cutModel: obj's meshes cut by plane (see above); cap: the openings
// closed. Each mesh's new geometry (swaps: [mesh, old, new]; not put in
// yet) and how it went: { removed (triangles beyond, whole or cut), caps,
// open (an opening that didn't close up: left so) }.
export function cutModel(obj, plane, { cap = true } = {}) {
  const swaps = [], r = { removed: 0, caps: 0, open: 0, kept: 0 };
  // (eps: as near as counts as on the plane, the same place: the model's size × 1e-7)
  const box = new THREE.Box3().setFromObject(obj), eps = Math.max(box.getSize(new THREE.Vector3()).length(), 1e-9) * 1e-7;
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const g = o.geometry.index ? o.geometry.toNonIndexed() : o.geometry;
    const res = cutMesh(o, g, plane, eps, cap);
    r.removed += res.removed; r.caps += res.caps; r.open += res.open; r.kept += res.kept;
    if (res.geo) swaps.push([o, o.geometry, res.geo]);
  });
  return { swaps, ...r };
}

function cutMesh(o, g, plane, eps, cap) {
  const names = Object.keys(g.attributes), attrs = names.map((n) => g.getAttribute(n));
  const pos = g.getAttribute("position"), N = pos.count, T = Math.floor(N / 3);
  const groups = g.groups.length ? g.groups : [{ start: 0, count: N, materialIndex: 0 }];
  const M = o.matrixWorld;
  // A corner: its place (world), distance, and attributes (as stored).
  const corner = (i) => {
    const w = new THREE.Vector3().fromBufferAttribute(pos, i).applyMatrix4(M);
    let d = plane.distanceToPoint(w);
    if (Math.abs(d) < eps) d = 0;
    return { w, d, a: attrs.map((at) => Array.from(at.array.subarray(i * at.itemSize, (i + 1) * at.itemSize))) };
  };
  const lerp = (p, q, t) => ({ w: p.w.clone().lerp(q.w, t), d: 0, a: p.a.map((v, k) => v.map((x, j) => x + (q.a[k][j] - x) * t)) });
  // cross: where edge p–q crosses the plane, worked out from its lesser end
  // (by place) whichever triangle it's in: the same point both sides (welded).
  const less = (p, q) => p.w.x < q.w.x || (p.w.x === q.w.x && (p.w.y < q.w.y || (p.w.y === q.w.y && p.w.z < q.w.z)));
  const cross = (p, q) => (less(q, p) ? cross(q, p) : lerp(p, q, p.d / (p.d - q.d)));
  // The triangles kept (the side not taken away, the pieces of those cut),
  // each [c0, c1, c2, group].
  const tris = [];
  let removed = 0;
  groups.forEach((gr, gi) => {
    for (let t = gr.start / 3; t < Math.min(T, (gr.start + gr.count) / 3); t++) {
      const cs = [corner(t * 3), corner(t * 3 + 1), corner(t * 3 + 2)];
      if (cs.every((c) => c.d <= 0)) { tris.push([...cs, gi]); continue; }
      removed++;
      if (cs.every((c) => c.d >= 0)) continue;
      const poly = [];
      for (let i = 0; i < 3; i++) {
        const j = (i + 1) % 3, p = cs[i], q = cs[j];
        if (p.d <= 0) poly.push(p);
        if ((p.d < 0 && q.d > 0) || (p.d > 0 && q.d < 0)) poly.push(cross(p, q));
      }
      for (let i = 1; i + 1 < poly.length; i++) tris.push([poly[0], poly[i], poly[i + 1], gi]);
    }
  });
  if (!removed) return { geo: null, removed: 0, caps: 0, open: 0, kept: T };
  // Its corners welded (the same place: one vertex), for what's joined.
  const ids = new Map(), at = [];
  const vid = (c) => {
    const k = `${Math.round(c.w.x / eps)},${Math.round(c.w.y / eps)},${Math.round(c.w.z / eps)}`;
    let v = ids.get(k);
    if (v === undefined) { v = at.length; ids.set(k, v); at.push(c); }
    return v;
  };
  const tv = tris.map((tr) => [vid(tr[0]), vid(tr[1]), vid(tr[2])]);
  // The pieces (triangles joined by a corner, on and on).
  const parent = at.map((_, i) => i), find = (i) => { while (parent[i] !== i) i = parent[i] = parent[parent[i]]; return i; };
  for (const [a, b, c] of tv) { parent[find(b)] = find(a); parent[find(c)] = find(a); }
  // The openings on the plane: the edges on it with no triangle the other
  // way, chained into loops (the cap's way round: each edge reversed), each
  // on the plane's own axes (u, v) and with its piece.
  const on = (v) => at[v].d === 0, edges = new Set(), groupOf = new Map(), next = new Map();
  tv.forEach((t) => { for (let k = 0; k < 3; k++) edges.add(`${t[k]},${t[(k + 1) % 3]}`); });
  tv.forEach((t, i) => {
    for (let k = 0; k < 3; k++) {
      const a = t[k], b = t[(k + 1) % 3];
      if (a === b || !on(a) || !on(b) || edges.has(`${b},${a}`)) continue;
      (next.get(b) || next.set(b, []).get(b)).push(a);
      groupOf.set(b, tris[i][3]);
    }
  });
  const nrm = plane.normal, u = new THREE.Vector3(), v = new THREE.Vector3();
  u.copy(Math.abs(nrm.x) < 0.9 ? new THREE.Vector3(1, 0, 0) : new THREE.Vector3(0, 1, 0)).cross(nrm).normalize();
  v.crossVectors(nrm, u);
  const loops = [], chains = [];
  let open = 0;
  const closed = (loop) => {
    while (loop.length > 3 && at[loop[loop.length - 1]].w.equals(at[loop[0]].w)) loop.pop(); // (its start again at the end: once; see triangulateShape)
    const pts = loop.map((i) => new THREE.Vector2(at[i].w.dot(u), at[i].w.dot(v)));
    loops.push({ loop, pts, area: THREE.ShapeUtils.area(pts), piece: find(loop[0]) });
  };
  for (const start of [...next.keys()]) {
    while (next.get(start)?.length) {
      const loop = [start];
      let cur = next.get(start).pop(), ok = false;
      for (let guard = 0; guard < at.length + 1; guard++) {
        if (cur === start) { ok = true; break; }
        loop.push(cur);
        const nx = next.get(cur);
        if (!nx?.length) break;
        cur = nx.pop();
      }
      if (ok && loop.length >= 3) closed(loop);
      else if (!ok) chains.push(loop);
    }
  }
  // The ones that didn't close (a gap in the model's own surface where the
  // plane crosses it, or a piece started midway): joined end to the
  // nearest start, across a small gap at most, till they close.
  const gap = eps * 2e5; // (2% of the model's size)
  while (chains.length) {
    let c = chains.pop();
    for (;;) {
      const end = at[c[c.length - 1]].w;
      let best = -1, bd = end.distanceTo(at[c[0]].w); // (-1: its own start)
      chains.forEach((o, i) => { const d = end.distanceTo(at[o[0]].w); if (d < bd) { bd = d; best = i; } });
      if (bd > gap) { open++; break; }
      if (best < 0) { if (c.length >= 3) closed(c); else open++; break; }
      const o = chains.splice(best, 1)[0];
      c = c.concat(o[0] === c[c.length - 1] ? o.slice(1) : o);
    }
  }
  const inside = (p, poly) => { let c = false; for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) { const a = poly[i], b = poly[j]; if ((a.y > p.y) !== (b.y > p.y) && p.x < ((b.x - a.x) * (p.y - a.y)) / (b.y - a.y) + a.x) c = !c; } return c; };
  const keep = tris.map(() => true), gone = new Set();
  // Each opening left closed, flat: a loop within an even number of others
  // an outline, within an odd number a hole in the one round it (a ring's
  // middle: kept open); its triangles wound as the outline's loop is (as
  // the surface round it: whichever way that faces).
  const capTris = []; // [[corner, corner, corner], group]
  let caps = 0;
  if (cap) {
    const left = loops.filter((l) => !gone.has(l.piece));
    for (const l of left) l.depth = left.filter((o) => o !== l && Math.abs(o.area) > Math.abs(l.area) && inside(l.pts[0], o.pts)).length;
    const outers = left.filter((l) => l.depth % 2 === 0);
    for (const h of left.filter((l) => l.depth % 2 === 1)) {
      let best = null;
      for (const ol of outers) if (ol.depth === h.depth - 1 && inside(h.pts[0], ol.pts) && (!best || Math.abs(ol.area) < Math.abs(best.area))) best = ol;
      if (best) (best.holes ||= []).push(h); else open++;
    }
    for (const ol of outers) {
      const hs = ol.holes || [], all = [ol, ...hs], ptsAll = all.flatMap((l) => l.pts), vAll = all.flatMap((l) => l.loop), sign = Math.sign(ol.area);
      const faces = THREE.ShapeUtils.triangulateShape(ol.pts, hs.map((h) => h.pts));
      const gi = groupOf.get(ol.loop[0]) ?? 0;
      for (let [a, b, c] of faces) {
        if (Math.sign(THREE.ShapeUtils.area([ptsAll[a], ptsAll[b], ptsAll[c]])) !== sign) [b, c] = [c, b];
        capTris.push([[at[vAll[a]], at[vAll[b]], at[vAll[c]]], gi]);
      }
      caps++;
    }
  }
  // Out: by group, as they were, each cap in its loop's group.
  const nLocal = plane.normal.clone().applyMatrix3(new THREE.Matrix3().setFromMatrix4(M).transpose()).normalize();
  const outs = names.map(() => []), ng = [];
  const ni = names.indexOf("normal"), ui = names.indexOf("uv");
  let count = 0, kept = 0;
  groups.forEach((gr, gi) => {
    const start = count;
    tris.forEach((tr, i) => {
      if (!keep[i] || tr[3] !== gi) return;
      for (let k = 0; k < 3; k++) tr[k].a.forEach((vals, j) => outs[j].push(...vals));
      count += 3; kept++;
    });
    for (const [cs, cg] of capTris) {
      if (cg !== gi) continue;
      for (const c of cs) c.a.forEach((vals, j) => {
        if (j === ni) outs[j].push(nLocal.x, nLocal.y, nLocal.z);
        else if (j === ui) outs[j].push(...cs[0].a[j]); // (one spot of its texture each: its edge's color, not stretched across)
        else outs[j].push(...vals);
      });
      count += 3;
    }
    if (g.groups.length) ng.push({ start, count: count - start, materialIndex: gr.materialIndex });
  });
  const geo = new THREE.BufferGeometry();
  names.forEach((name, k) => {
    const arr = new Float32Array(outs[k]);
    if (name === "normal") for (let i = 0; i < arr.length; i += 3) { const l = Math.hypot(arr[i], arr[i + 1], arr[i + 2]) || 1; arr[i] /= l; arr[i + 1] /= l; arr[i + 2] /= l; }
    geo.setAttribute(name, new THREE.BufferAttribute(arr, attrs[k].itemSize, attrs[k].normalized));
  });
  for (const gr of ng) geo.addGroup(gr.start, gr.count, gr.materialIndex);
  return { geo, removed, caps, open, kept };
}

// flattenModel: obj's meshes' face plane is on (its normal: out of it)
// made flat on it (see above). Each mesh's new geometry (swaps, as
// cutModel's) and how it went: { moved (points), hill, dip (the furthest
// one pulled back, out: m) }.
export function flattenModel(obj, plane) {
  const swaps = [], r = { moved: 0, hill: 0, dip: 0 };
  const size = Math.max(new THREE.Box3().setFromObject(obj).getSize(new THREE.Vector3()).length(), 1e-9);
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const g = o.geometry.index ? o.geometry.toNonIndexed() : o.geometry;
    const res = flattenMesh(o, g, plane, size);
    r.moved += res.moved; r.hill = Math.max(r.hill, res.hill); r.dip = Math.max(r.dip, res.dip);
    if (res.geo) swaps.push([o, o.geometry, res.geo]);
  });
  return { swaps, ...r };
}

// (FACING: how squarely a triangle faces the plane's way to be the face's;
// a 45° bevel's is 0.71, a wall's 0)
const FACING = 0.3;

function flattenMesh(o, g, plane, size) {
  const pos = g.getAttribute("position"), N = pos.count, T = Math.floor(N / 3), M = o.matrixWorld, D = plane.normal;
  const eps = size * 1e-7, band = size * 0.005;
  // Its points (world), welded: a corner's vertex, each vertex's place and distance.
  const w = new Float64Array(N * 3), ids = new Map(), vid = new Int32Array(N), vp = [];
  const p = new THREE.Vector3();
  for (let i = 0; i < N; i++) {
    p.fromBufferAttribute(pos, i).applyMatrix4(M);
    w[i * 3] = p.x; w[i * 3 + 1] = p.y; w[i * 3 + 2] = p.z;
    const k = `${Math.round(p.x / eps)},${Math.round(p.y / eps)},${Math.round(p.z / eps)}`;
    let v = ids.get(k);
    if (v === undefined) { v = vp.length; ids.set(k, v); vp.push(p.clone()); }
    vid[i] = v;
  }
  const dist = vp.map((q) => plane.distanceToPoint(q));
  // The face's triangles: facing the plane's way (either winding: a scan's
  // may face in) and seen from that side (nothing of the model past them).
  const bvh = new MeshBVH(g, { indirect: true }), inv = M.clone().invert(), dl = D.clone().transformDirection(inv);
  const ray = new THREE.Ray(), a = new THREE.Vector3(), b = new THREE.Vector3(), c = new THREE.Vector3(), n = new THREE.Vector3();
  const face = new Uint8Array(T);
  for (let t = 0; t < T; t++) {
    a.fromArray(w, t * 9); b.fromArray(w, t * 9 + 3); c.fromArray(w, t * 9 + 6);
    n.subVectors(c, b).cross(a.clone().sub(b));
    const len = n.length();
    if (!len || Math.abs(n.dot(D)) / len < FACING) continue;
    a.add(b).add(c).divideScalar(3).addScaledVector(D, size * 1e-5); // (its middle, just off it)
    ray.origin.copy(a).applyMatrix4(inv);
    ray.direction.copy(dl);
    if (!bvh.raycastFirst(ray, THREE.DoubleSide)) face[t] = 1;
  }
  // Joined to where the plane is: from the face's triangles at it (across
  // it, or within a little of it), on through the face's.
  const vt = new Map();
  for (let t = 0; t < T; t++) if (face[t]) for (let k = 0; k < 3; k++) { const v = vid[t * 3 + k]; (vt.get(v) || vt.set(v, []).get(v)).push(t); }
  const on = new Uint8Array(T), q = [];
  for (let t = 0; t < T; t++) {
    if (!face[t]) continue;
    const ds = [0, 1, 2].map((k) => dist[vid[t * 3 + k]]);
    if (Math.min(...ds) <= 0 && Math.max(...ds) >= 0 || Math.min(...ds.map(Math.abs)) <= band) { on[t] = 1; q.push(t); }
  }
  while (q.length) {
    const t = q.pop();
    for (let k = 0; k < 3; k++) for (const u of vt.get(vid[t * 3 + k]) || []) if (!on[u]) { on[u] = 1; q.push(u); }
  }
  // Its points on the plane (in the mesh's own space).
  const move = new Uint8Array(vp.length);
  for (let t = 0; t < T; t++) if (on[t]) for (let k = 0; k < 3; k++) move[vid[t * 3 + k]] = 1;
  let moved = 0, hill = 0, dip = 0;
  const to = vp.map((q, v) => {
    if (!move[v] || dist[v] === 0) return null;
    moved++;
    if (dist[v] > 0) hill = Math.max(hill, dist[v]); else dip = Math.max(dip, -dist[v]);
    return q.clone().addScaledVector(D, -dist[v]).applyMatrix4(inv);
  });
  if (!moved) return { geo: null, moved: 0, hill: 0, dip: 0 };
  const out = g.clone(), op = out.getAttribute("position"), nrm = out.getAttribute("normal"), touched = new Uint8Array(T);
  for (let i = 0; i < N; i++) {
    const t2 = to[vid[i]];
    if (!t2) continue;
    op.setXYZ(i, t2.x, t2.y, t2.z);
    touched[(i / 3) | 0] = 1;
  }
  // The triangles moved: flat normals again, the way their own were.
  if (nrm) for (let t = 0; t < T; t++) {
    if (!touched[t]) continue;
    a.fromBufferAttribute(op, t * 3); b.fromBufferAttribute(op, t * 3 + 1); c.fromBufferAttribute(op, t * 3 + 2);
    n.subVectors(c, b).cross(a.sub(b)).normalize();
    p.set(nrm.getX(t * 3) + nrm.getX(t * 3 + 1) + nrm.getX(t * 3 + 2), nrm.getY(t * 3) + nrm.getY(t * 3 + 1) + nrm.getY(t * 3 + 2), nrm.getZ(t * 3) + nrm.getZ(t * 3 + 1) + nrm.getZ(t * 3 + 2));
    if (n.dot(p) < 0) n.negate();
    for (let k = 0; k < 3; k++) nrm.setXYZ(t * 3 + k, n.x, n.y, n.z);
  }
  out.computeBoundingBox();
  out.computeBoundingSphere();
  return { geo: out, moved, hill, dip };
}
