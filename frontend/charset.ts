/**
 * RetroTxtJS
 * js/module/charset.js
 * © Ben Garrett, code.by.ben@gmail.com
 */

export enum CodePage {
  DOS437Ctrls = "cp437_C0", // IBM PC/MS-DOS control codes
  DOS437En = "cp_437", // IBM PC/MS-DOS English legacy text
  ISO88591 = "iso_8859_1", // Commodore Amiga and Unix English legacy text
  Win1252EN = "cp_1252", // Windows 3.1/9x era legacy text
  Win1252R8R9 = "cp_1252_r8_r9", // Windows 1252 rows eight and nine
}

/**
 * An internal table of Unicode characters that emulate Code Page 437.
 * ASCII C0 controls are replaced with characters.
 * Sets 2 through 7 are standard characters that are identical in both
 * ASCII and Unicode.
 */
const set0 = Array.from(`␀☺☻♥♦♣♠•◘○◙♂♀♪♫☼`);
const set1 = Array.from(`►◄↕‼¶§▬↨↑↓→←∟↔▲▼`);
const set8 = Array.from(`ÇüéâäàåçêëèïîìÄÅ`);
const set9 = Array.from(`ÉæÆôöòûùÿÖÜ¢£¥₧ƒ`);
const setA = Array.from(`áíóúñÑªº¿⌐¬½¼¡«»`);
const setB = Array.from(`░▒▓│┤╡╢╖╕╣║╗╝╜╛┐`);
const setC = Array.from(`└┴┬├─┼╞╟╚╔╩╦╠═╬╧`);
const setD = Array.from(`╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀`);
const setE = Array.from(`αßΓπΣσµτΦΘΩδ∞φε∩`);
const setF = Array.from(`≡±≥≤⌠⌡÷≈°∙·√ⁿ²■\u00A0`);

export const CharacterSet = {
  // prettier-ignore
  [CodePage.DOS437En]: [...set8, ...set9, ...setA, ...setB, ...setC, ...setD, ...setE, ...setF],
  [CodePage.DOS437Ctrls]: [...set0, ...set1],
  [CodePage.ISO88591]: iso88591(),
  [CodePage.Win1252EN]: cp1252(),
  [CodePage.Win1252R8R9]: cp1252Table(),
};

/**
 * Unicode characters that emulate ISO 8859-1.
 */
function iso88591(): Array<string> {
  const sp = 32,
    tilde = 126,
    nbsp = 160,
    ÿ = 255;
  const undefinePoint = (i: number) => {
    if (i < sp) return true;
    if (i > tilde && i < nbsp) return true;
    if (i > ÿ) return true;
    return false;
  };
  const empty = ` `;
  let iso = [];
  for (let i = 0; i <= ÿ; i++) {
    if (undefinePoint(i)) {
      iso = [...iso, empty];
      continue;
    }
    iso = [...iso, String.fromCharCode(i)];
  }
  return iso;
}

/**
 * Returns a partial table of code page Windows-1252 matching characters.
 * Only rows 8 and 9 are returned as all other characters match ISO-8859-1
 * which is already supported by JavaScript.
 */
function cp1252Table(): Array<string> {
  // prettier-ignore
  const set8 = [`€`,``,`‚`,`ƒ`,`„`,`…`,`†`,`‡`,`ˆ`,`‰`,`Š`,`‹`,`Œ`,``,`Ž`,``]
  // prettier-ignore
  const set9 = [``,`‘`,`’`,`“`,`”`,`•`,`–`,`—`,`\u02dc`,`™`,`š`,`›`,`œ`,``,`ž`,`Ÿ`]
  return [...set8, ...set9];
}

/**
 * Unicode characters that emulate code page Windows-1252.
 */
function cp1252(): Array<string> {
  const euro = 128,
    Ÿ = 159,
    ÿ = 255;
  let cp = [];
  for (let i = 0; i <= ÿ; i++) {
    if (i === euro) {
      cp = [...cp, ...cp1252Table()];
      i = Ÿ;
      continue;
    }
    cp = [...cp, String.fromCharCode(i)];
  }
  return cp;
}
