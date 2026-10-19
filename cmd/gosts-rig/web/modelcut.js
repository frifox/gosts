// Cut (the model editor's): the model sliced by a plane (XY, XZ or YZ),
// either side of it taken away (Cut), or the uneven face it crosses made
// flat on it (Flatten, fill here: a scanned floor that's dished or rounded off, say).
//
// The plane is in the world (as the model's shown, edited): a three.js
// Plane whose normal points to the side to take away (beyond: distance >
// 0). Each mesh's triangles are split where they cross it; the pieces
// beyond go. Fill takes away the craters too: the parts left that hang
// off the plane's line round them only, facing the side taken away (by
// their area: a closed body's sums to nothing, one cut open by the plane
// faces away from the opening, a crater towards it). Then each opening
// left on the plane is closed: flat, its holes (a ring's middle) kept.
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

// cutModel: obj's meshes cut by plane (see above); fill: Fill's craters
// taken too; cap: the openings closed (Fill: always). Each mesh's new
// geometry (swaps: [mesh, old, new]; not put in yet) and how it went:
// { removed (triangles beyond, whole or cut), craters, caps, open (an
// opening that didn't close up: left so) }.
export function cutModel(obj, plane, { fill = false, cap = true } = {}) {
  const swaps = [], r = { removed: 0, craters: 0, caps: 0, open: 0, kept: 0 };
  // (eps: as near as counts as on the plane, the same place: the model's size × 1e-7)
  const box = new THREE.Box3().setFromObject(obj), eps = Math.max(box.getSize(new THREE.Vector3()).length(), 1e-9) * 1e-7;
  obj.traverse((o) => {
    if (!o.isMesh) return;
    const g = o.geometry.index ? o.geometry.toNonIndexed() : o.geometry;
    const res = cutMesh(o, g, plane, eps, fill, cap || fill);
    r.removed += res.removed; r.craters += res.craters; r.caps += res.caps; r.open += res.open; r.kept += res.kept;
    if (res.geo) swaps.push([o, o.geometry, res.geo]);
  });
  return { swaps, ...r };
}

function cutMesh(o, g, plane, eps, fill, cap) {
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
  if (!removed) return { geo: null, removed: 0, craters: 0, caps: 0, open: 0, kept: T };
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
  // Fill: the craters taken too: a piece of its own whose openings on the
  // plane are all within another piece's (a dish in a floor, rimmed by it).
  // (Which way its triangles face: not asked; a scan's may face in.)
  let keep = tris.map(() => true), craters = 0;
  const gone = new Set();
  if (fill) {
    const byPiece = new Map();
    for (const l of loops) (byPiece.get(l.piece) || byPiece.set(l.piece, []).get(l.piece)).push(l);
    for (const [piece, ls] of byPiece) if (ls.every((l) => loops.some((o) => o.piece !== piece && inside(l.pts[0], o.pts)))) gone.add(piece);
    craters = gone.size;
    keep = tv.map((t) => !gone.has(find(t[0])));
  }
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
  return { geo, removed, craters, caps, open, kept };
}
