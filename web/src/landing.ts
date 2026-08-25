import "./landing.css";

/**
 * Draws the punch card that fills the hero.
 *
 * A real card is 80 columns by 12 rows of rectangular holes. Drawing it rather
 * than shipping a photograph means it costs nothing to load, scales to any
 * screen without a second asset, and — the reason that matters — it can carry
 * real information instead of being decoration.
 *
 * The punched columns are not random. They spell out the product's own sentence
 * in the encoding the machine actually used, which is the sort of detail nobody
 * will notice and everybody would feel the absence of.
 */

const COLUMNS = 80;
const ROWS = 12;

/**
 * IBM 029 keypunch encoding, trimmed to what this string needs.
 *
 * Rows on a card run 12, 11, 0, 1…9 from the top, which is why the zone punches
 * are the first three indices rather than where you would expect them.
 */
const ZONE_12 = 0;
const ZONE_11 = 1;
const ZONE_0 = 2;
const digitRow = (d: number) => ZONE_0 + d; // 1 → 3, 9 → 11

function punchesFor(char: string): number[] {
  const c = char.toUpperCase();
  if (c === " ") return [];
  if (c === "0") return [ZONE_0];
  if (c >= "1" && c <= "9") return [digitRow(Number(c))];
  if (c >= "A" && c <= "I") return [ZONE_12, digitRow(c.charCodeAt(0) - 64)];
  if (c >= "J" && c <= "R") return [ZONE_11, digitRow(c.charCodeAt(0) - 73)];
  if (c >= "S" && c <= "Z") return [ZONE_0, digitRow(c.charCodeAt(0) - 81)];
  if (c === ".") return [ZONE_12, digitRow(3), digitRow(8)];
  return [];
}

const MESSAGE = "PUNCHCARD. TIME TRACKING THAT SHOWS ITS WORK. THE COMMITS COME WITH THE HOURS.";

function draw() {
  const svg = document.getElementById("field");
  if (!(svg instanceof SVGElement)) return;

  // Card proportions, not screen proportions. A punch card whose holes are
  // square is not a punch card.
  const w = 5;
  const h = 11;
  const gapX = 2.6;
  const gapY = 3.4;
  const width = COLUMNS * (w + gapX);
  const height = ROWS * (h + gapY);
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);

  const parts: string[] = [];
  for (let col = 0; col < COLUMNS; col++) {
    const punched = punchesFor(MESSAGE[col] ?? " ");
    for (let row = 0; row < ROWS; row++) {
      const x = col * (w + gapX);
      const y = row * (h + gapY);
      const on = punched.includes(row);
      parts.push(
        `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="1.5" class="${on ? "on" : "off"}"/>`,
      );
    }
  }
  svg.innerHTML = parts.join("");
}

draw();
