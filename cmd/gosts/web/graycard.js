// Balancing white from a gray card: what colour temperature (Kelvin) makes
// the card come out neutral.
//
// A camera set to T_set multiplies its colours so that light of T_set comes
// out neutral; under light of T_real, a gray card then comes out tinted by
// the ratio of the two lights' colours. So on the red-blue axis
//   f(T_real) = (card red/blue) × f(T_set)
// where f(T) is a black body's red/blue at T (linear sRGB, the Planckian
// locus after Kim et al. 2002), and T_real is what to set. The camera's own
// colours aren't quite sRGB, so the answer is close, not exact: a second
// sample at the new setting brings it the rest of the way. Kelvin moves
// along blue-amber only: a green or magenta cast (some LEDs) it can't fix,
// and greenCast says so.

// planckXY is the chromaticity (x, y) of a black body at T kelvin (1667–25000).
export function planckXY(T) {
  const t = Math.min(25000, Math.max(1667, T)), t2 = t * t, t3 = t2 * t;
  const x = t <= 4000
    ? -0.2661239e9 / t3 - 0.2343589e6 / t2 + 0.8776956e3 / t + 0.179910
    : -3.0258469e9 / t3 + 2.1070379e6 / t2 + 0.2226347e3 / t + 0.240390;
  const x2 = x * x, x3 = x2 * x;
  const y = t <= 2222 ? -1.1063814 * x3 - 1.34811020 * x2 + 2.18555832 * x - 0.20219683
    : t <= 4000 ? -0.9549476 * x3 - 1.37418593 * x2 + 2.09137015 * x - 0.16748867
    : 3.0817580 * x3 - 5.87338670 * x2 + 3.75112997 * x - 0.37001483;
  return [x, y];
}

// planckRGB is a black body's colour at T kelvin, linear sRGB (Y = 1).
export function planckRGB(T) {
  const [x, y] = planckXY(T);
  const X = x / y, Y = 1, Z = (1 - x - y) / y;
  return [
    3.2406 * X - 1.5372 * Y - 0.4986 * Z,
    -0.9689 * X + 1.8758 * Y + 0.0415 * Z,
    0.0557 * X - 0.2040 * Y + 1.0570 * Z,
  ];
}

const rb = (T) => { const [r, , b] = planckRGB(T); return r / b; };

// linear turns an sRGB value (0–255) into linear light (0–1).
export function linear(v) {
  const c = v / 255;
  return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}

// presetKelvin is roughly the temperature a white balance preset stands for
// (Sony's, as gphoto2 names them); undefined for those that aren't one
// (Automatic: the camera's own guess, unknown).
export const presetKelvin = {
  "Daylight": 5500, "Shade": 7500, "Cloudy": 6500, "Tungsten": 3200, "Flash": 5500,
  "Fluorescent: Warm White": 3000, "Fluorescent: Cold White": 4000,
  "Fluorescent: Day White": 5000, "Fluorescent: Daylight": 6500,
};

// problem is why a card's colour can't be measured ("" if it can).
export function problem(srgb) {
  if (Math.max(...srgb) > 250) return "the card's too bright (clipped): darken the exposure and take another sample";
  if (Math.min(...srgb) < 8) return "the card's too dark: brighten the exposure and take another sample";
  return "";
}

// White balance shifts (the camera's A–B and G–M grid), in quarter steps
// towards B and M. One moves a colour ratio by SHIFT_LN in log terms:
// measured on the A6600, from samples at each end of both (A7 to B7 took a
// scene's red/blue from 2.15 to 0.47; G7 to M7 its green/(red·blue)^½ from
// 2.53 to 0.54: ln of either ≈ 1.53 over 56 quarter steps).
export const SHIFT_LN = 0.0275;

// shifts are the moves (quarter steps, towards B and M) that make a card
// whose average colour came out as srgb neutral, from the shifts it was
// taken with: ab (blue against red: what colour temperature also does) and
// gm. Not rounded or limited to the camera's steps.
export function shifts(srgb) {
  const [r, g, b] = srgb.map(linear);
  return { ab: Math.log(r / b) / SHIFT_LN, gm: Math.log(g / Math.sqrt(r * b)) / SHIFT_LN };
}

// castShift is the G–M move (quarter steps towards M) that takes out a cast
// as balance reports it.
export const castShift = (cast) => Math.log(1 + cast) / SHIFT_LN;

// balance works out the colour temperature for a card whose average colour
// came out as srgb ([r, g, b], 0–255) with the camera set to setK kelvin.
// It returns { kelvin, warmer (true: the light's warmer than setK), cast
// (green, + or magenta, −, left over: 0 is none), problem (a reason it can't
// be trusted, or "") }.
export function balance(srgb, setK) {
  const [r, g, b] = srgb.map(linear);
  const bad = problem(srgb);
  if (bad) return { problem: bad };
  const want = (r / b) * rb(setK);
  // f falls as T rises: find T with f(T) = want (from 2000 K: below that a
  // black body's blue is out of sRGB, under 0).
  let lo = 2000, hi = 20000;
  if (want >= rb(lo) || want <= rb(hi)) return { problem: "the card is too far off to measure: is it the gray card, and the light a normal white?" };
  for (let i = 0; i < 60; i++) {
    const mid = (lo + hi) / 2;
    if (rb(mid) > want) lo = mid; else hi = mid;
  }
  const kelvin = (lo + hi) / 2;
  // Green against the red-blue mean, as the new setting would leave it.
  const [pr, pg, pb] = planckRGB(kelvin), [sr, sg, sb] = planckRGB(setK);
  const nr = r * sr / pr, ng = g * sg / pg, nb = b * sb / pb; // the card at the new setting
  const cast = ng / Math.sqrt(nr * nb) - 1;
  return { kelvin, warmer: kelvin < setK, cast, problem: "" };
}
