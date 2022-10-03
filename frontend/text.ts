/**
 * RetroTxtJS
 * js/module/text.js
 * © Ben Garrett, code.by.ben@gmail.com
 */
import { CharacterSet, CodePage } from "./charset.js";

const asciiTable = CharacterSet[CodePage.DOS437Ctrls];
const win1252Table = CharacterSet[CodePage.Win1252R8R9];

/**
 * Convert encoded binary strings to emulate a legacy code page.
 */
export function decodeText(
  encodedData: Uint8Array,
  codepage = "",
  displayControls = false
): string {
  let normalized = ``;
  const extendedTable = CharacterSet[codepage];
  // loop through text and use the values to propagate the container
  for (let i = 0; i < encodedData.byteLength; i++) {
    const c = fromCharCode(encodedData[i]);
    if (c !== '\r') {
      normalized += c;
    }
  }
  return normalized;

  /**
   * Looks up a character code and returns an equivalent Unicode symbol.
   * @param {*} number Hex or decimal character code
   * @returns {string} Unicode symbol
   */
  function fromCharCode(number: number): string {
    const NUL = 0,
      horizontalTab = 9,
      lineFeed = 10,
      carriageReturn = 13,
      escape = 27,
      US = 31,
      invalid = 65533;
    // handle oddball `NUL` characters that some docs use as a placeholder.
    // 65533 is used by the browser as an invalid or unknown character code.
    // the ␀ glyph used to be return but doesn't work well in monospace fonts
    if (number === NUL) return ` `;
    if (number === invalid) return ` `;
    // ASCII was originally 7-bits so could support a maximum of 128 characters.
    // interpret ASCII C0 controls as CP-437 symbols characters 0-31
    if (number >= NUL && number <= US) {
      // 0x1B is the escape character that is also used as a trigger for
      // ANSI escape codes
      if (number === escape) return asciiTable[number];
      // `displayControls` enabled will force the display of most CP-437 glyphs
      if (displayControls) {
        switch (number) {
          // return as an ASCII C0 control
          case horizontalTab:
            return `\t`;
          case lineFeed:
          case carriageReturn:
            return `\n`;
          default:
            // JavaScript also supports these escape codes, but in HTML they
            // have no effect
            // 08 BS \b - backspace
            // 11 VT \v - vertical tab
            // 12 FF \f - form feed
            // return all other ASCII C0 controls as CP437 glyphs
            if (codepage === CodePage.DOS437En) return asciiTable[number];
            return ` `;
        }
      }
      // RetroTxt option displayControls=disabled will return all C0 controls
      // return as an ASCII C0 control
      if (codepage === CodePage.DOS437En)
        return `${String.fromCharCode(number)}`;
      switch (number) {
        // return as an ASCII C0 control
        case horizontalTab:
          return `\t`;
        case lineFeed:
        case carriageReturn:
          return `\n`;
      }
      return ` `;
    }
    const space = 32,
      tilde = 126,
      deleted = 127;
    // characters 0x20 (32) through to 0x7E (126) are universal between
    // most code pages and so they are left as-is
    if (number >= space && number <= tilde)
      return `${String.fromCharCode(number)}`;
    // normally ASCII 0x7F (127) is the delete control
    // but in CP437 it can also represent a house character
    if (number === deleted && displayControls) return `⌂`;
    // ASCII extended are additional supported characters when ASCII is used in
    // an 8-bit set. All MS-DOS code pages are 8-bit and support the additional
    // 128 characters, between 8_0 (128)...F_F (255)
    switch (codepage) {
      case CodePage.DOS437En:
        return lookupCp437(number);
      case CodePage.ISO88591:
      case CodePage.Win1252EN:
        return lookupCharCode(number);
      default:
        throw new Error(`Unknown code page: "${codepage}"`);
    }
  }

  /**
   * A lookup for extended characters using Code Page 437 as the base table.
   * @param {*} number Hex or decimal character code
   * @returns {string} Unicode symbol
   */
  function lookupCp437(number: number): string {
    const nbsp = 0xa0,
      ÿ = 0xff,
      offset = 128;
    if (number >= nbsp && number <= ÿ)
      return extendedTable[number - offset];
    // check for unique Windows 1252 characters
    const win1252 = lookupRows8_9(number);
    // assume any values higher than 0xFF (255) are Unicode values that are ignored
    if ([``, `undefined`].includes(win1252)) return ``;
    // return match character
    return win1252;
  }

  /**
   * A lookup for extended characters using Windows 1252 in rows eight and nine.
   * @param {*} number Hex or decimal character code
   * @returns {string} Unicode symbol
   */
  function lookupRows8_9(number: number): string {
    const i = win1252Table.indexOf(String.fromCodePoint(number)),
      offset = 128;
    if (i === -1) return `${extendedTable[number - offset]}`;
    return `${extendedTable[i]}`;
  }

  /**
   * A lookup for extended characters.
   * @param {*} number Hex or decimal character code
   * @returns {string} Unicode symbol
   */
  function lookupCharCode(number: number): string {
    const euro = 0x80,
      ff = 0xff;
    if (number >= euro && number <= ff) return extendedTable[number];
    // assume any values higher than 0xFF (255) are Unicode values
    return ``;
  }
}
