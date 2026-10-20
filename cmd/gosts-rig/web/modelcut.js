// Cut (the model editor's): the model sliced by a plane (XY, XZ or YZ),
// either side of it taken away (Cut), or the hills on one side of it
// pushed onto it (Flatten: lumps in a scanned floor, say).
//
// The plane is in the world (as the model's shown, edited): a three.js
// Plane. Cut: its normal points to the side taken away (beyond: distance >
// 0); each mesh's triangles are split where they cross it, the pieces
// beyond go, and each opening left on the plane is closed, flat, its holes
// (a ring's middle) kept. Flatten: its normal points the way the hills
// are pushed (a floor's: down); the hills on its other side whose edges
// are all on it (closed in by it: not the walls) are pushed onto it.
import * as THREE from "three";

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
    if (Math.abs(d) < eps * 10) d = 0; // (on it: within a little more than the welding's (a point put on it before, stored as a float))
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
      // (on the plane itself (a face flattened onto it before): closing it
      // up, that's the cap's: taken, the cap covers it, not two faces there)
      if (cap && cs.every((c) => c.d === 0)) { removed++; continue; }
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

// flattenModel: obj's hills pushed down onto plane (its normal: the way
// they're pushed; a hill: on the other side). A hill is a piece of the
// surface past the plane that way whose edge is all on it, a loop the
// plane closes round it: not one with an edge of its own (an open scan's),
// nor the model's main body (its walls). Each mesh's new geometry (swaps,
// as cutModel's: its triangles cut where they cross the plane) and how it
// went: { hills, moved (points), high (the tallest one's height: m) }.
export function flattenModel(obj, plane) {
  const swaps = [], r = { hills: 0, moved: 0, high: 0 };
  const eps = Math.max(new THREE.Box3().setFromObject(obj).getSize(new THREE.Vector3()).length(), 1e-9) * 1e-7;
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const g = o.geometry.index ? o.geometry.toNonIndexed() : o.geometry;
    const res = flattenMesh(o, g, plane, eps);
    r.hills += res.hills; r.moved += res.moved; r.high = Math.max(r.high, res.high);
    if (res.geo) swaps.push([o, o.geometry, res.geo]);
  });
  return { swaps, ...r };
}

function flattenMesh(o, g, plane, eps) {
  const names = Object.keys(g.attributes), attrs = names.map((n) => g.getAttribute(n));
  const pos = g.getAttribute("position"), N = pos.count, T = Math.floor(N / 3);
  const groups = g.groups.length ? g.groups : [{ start: 0, count: N, materialIndex: 0 }];
  const M = o.matrixWorld, inv = M.clone().invert(), n = plane.normal;
  // A corner: its place (world), height over the plane (> 0: the hills'
  // side), and attributes (as stored).
  const corner = (i) => {
    const w = new THREE.Vector3().fromBufferAttribute(pos, i).applyMatrix4(M);
    let h = -plane.distanceToPoint(w);
    if (Math.abs(h) < eps * 10) h = 0; // (as cutMesh's)
    return { w, h, a: attrs.map((at) => Array.from(at.array.subarray(i * at.itemSize, (i + 1) * at.itemSize))) };
  };
  const less = (p, q) => p.w.x < q.w.x || (p.w.x === q.w.x && (p.w.y < q.w.y || (p.w.y === q.w.y && p.w.z < q.w.z)));
  const cross = (p, q) => {
    if (less(q, p)) return cross(q, p);
    const t = p.h / (p.h - q.h);
    return { w: p.w.clone().lerp(q.w, t), h: 0, a: p.a.map((v, k) => v.map((x, j) => x + (q.a[k][j] - x) * t)) };
  };
  // Every triangle, those across the plane cut in two along it; each
  // [c0, c1, c2, group, up (on the hills' side)].
  const tris = [];
  let across = 0;
  groups.forEach((gr, gi) => {
    for (let t = gr.start / 3; t < Math.min(T, (gr.start + gr.count) / 3); t++) {
      const cs = [corner(t * 3), corner(t * 3 + 1), corner(t * 3 + 2)];
      const up = cs.every((c) => c.h >= 0) && cs.some((c) => c.h > 0);
      if (up || cs.every((c) => c.h <= 0)) { tris.push([...cs, gi, up]); continue; }
      across++;
      for (const side of [1, -1]) {
        const poly = [];
        for (let i = 0; i < 3; i++) {
          const p = cs[i], q = cs[(i + 1) % 3];
          if (p.h * side >= 0) poly.push(p);
          if (p.h * q.h < 0) poly.push(cross(p, q));
        }
        for (let i = 1; i + 1 < poly.length; i++) tris.push([poly[0], poly[i], poly[i + 1], gi, side > 0]);
      }
    }
  });
  // Corners welded (the same place: one point), the hills' side's pieces.
  const ids = new Map(), at = [];
  const vid = (c) => {
    const k = `${Math.round(c.w.x / eps)},${Math.round(c.w.y / eps)},${Math.round(c.w.z / eps)}`;
    let v = ids.get(k);
    if (v === undefined) { v = at.length; ids.set(k, v); at.push(c); }
    return v;
  };
  const tv = tris.map((tr) => [vid(tr[0]), vid(tr[1]), vid(tr[2])]);
  const parent = at.map((_, i) => i), find = (i) => { while (parent[i] !== i) i = parent[i] = parent[parent[i]]; return i; };
  // (joined across an edge, not just at a point: a hill touching the walls
  // at a corner on the plane is still a hill of its own)
  const used = new Map(); // (an edge of the hills' side's triangles: how many, and one of them)
  tv.forEach((t, i) => {
    if (!tris[i][4]) return;
    for (let k = 0; k < 3; k++) {
      const a = t[k], b = t[(k + 1) % 3], e = a < b ? `${a},${b}` : `${b},${a}`, u = used.get(e);
      if (u) { u.n++; parent[find(t[0])] = find(tv[u.t][0]); } else used.set(e, { n: 1, t: i });
    }
  });
  const pieceOf = (i) => find(tv[i][0]);
  // Each piece: its triangles; open if an edge of it isn't on the plane and
  // has nothing the other side (the model's own edge), or meets the other
  // side's triangles off the plane (it isn't cut off by it).
  const pieces = new Map();
  tv.forEach((t, i) => { if (tris[i][4]) { const p = pieceOf(i); (pieces.get(p) || pieces.set(p, { tris: 0, open: false }).get(p)).tris++; } });
  for (const [e, u] of used) {
    if (u.n !== 1) continue;
    const [a, b] = e.split(",").map(Number);
    if (at[a].h !== 0 || at[b].h !== 0) pieces.get(pieceOf(u.t)).open = true;
  }
  // The hills: the closed pieces, but the biggest (the model itself).
  let main = null;
  for (const [p, pc] of pieces) if (main === null || pc.tris > pieces.get(main).tris) main = p;
  const hill = new Set([...pieces].filter(([p, pc]) => !pc.open && p !== main).map(([p]) => p));
  const pts = new Set();
  let high = 0;
  tv.forEach((t, i) => { if (tris[i][4] && hill.has(pieceOf(i))) for (const v of t) if (at[v].h > 0) { pts.add(v); high = Math.max(high, at[v].h); } });
  const moved = pts.size;
  if (!hill.size) return { geo: null, hills: 0, moved: 0, high: 0 };
  // Out: every triangle, by group; a hill's corners on the plane (pushed
  // along its normal), its triangles facing as they did, flat.
  const ni = names.indexOf("normal"), pi = names.indexOf("position");
  const outs = names.map(() => []), ng = [];
  const nl = n.clone().applyMatrix3(new THREE.Matrix3().setFromMatrix4(M).transpose()).normalize(); // (the push, in its own space: as a normal)
  let count = 0;
  const e1 = new THREE.Vector3(), e2 = new THREE.Vector3();
  groups.forEach((gr, gi) => {
    const start = count;
    tris.forEach((tr, i) => {
      if (tr[3] !== gi) return;
      const flat = tr[4] && hill.has(pieceOf(i));
      let fn = null;
      if (flat && ni >= 0) { // (its normal: the push's, or against it, as its own were)
        const s = tr[0].a[ni].map((x, j) => x + tr[1].a[ni][j] + tr[2].a[ni][j]);
        e1.set(...s);
        fn = e1.dot(nl) >= 0 ? nl : nl.clone().negate();
      }
      for (let k = 0; k < 3; k++) {
        const c = tr[k];
        c.a.forEach((vals, j) => {
          if (flat && j === pi) { e2.copy(c.w).addScaledVector(n, c.h).applyMatrix4(inv); outs[j].push(e2.x, e2.y, e2.z); }
          else if (fn && j === ni) outs[j].push(fn.x, fn.y, fn.z);
          else outs[j].push(...vals);
        });
      }
      count += 3;
    });
    if (g.groups.length) ng.push({ start, count: count - start, materialIndex: gr.materialIndex });
  });
  const geo = new THREE.BufferGeometry();
  names.forEach((name, k) => {
    const arr = new Float32Array(outs[k]);
    if (name === "normal") for (let i = 0; i < arr.length; i += 3) { const l = Math.hypot(arr[i], arr[i + 1], arr[i + 2]) || 1; arr[i] /= l; arr[i + 1] /= l; arr[i + 2] /= l; }
    geo.setAttribute(name, new THREE.BufferAttribute(arr, attrs[k].itemSize, attrs[k].normalized));
  });
  for (const gr of ng) geo.addGroup(gr.start, gr.count, gr.materialIndex);
  geo.computeBoundingBox();
  geo.computeBoundingSphere();
  return { geo, hills: hill.size, moved, high };
}
