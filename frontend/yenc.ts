import * as crc32 from "crc-32";
import * as bytes from "./bytes";
import { ByteWriter } from "@stablelib/bytewriter";

export interface yFile {
  name: string; // Name of the final output file
  size: number; // Final, overall file size (of all parts decoded)
  part: number; // Part number (starts from 1)
  total: number; // Total number of parts. Optional even for multipart.
  line: number; // Average line length
  begin: number; // Part begin offset (0-indexed). Note the begin keyword in the =ypart line is 1-indexed.
  end: number; // Part end offset (0-indexed, exclusive)
  data: Uint8Array;
}

export const LineMax = 128;

export function decode(encoded: Uint8Array, crc32seed?: number): yFile {
  let s = State.Start;
  const file: Partial<yFile> = {};
  readHeader();
  decodeData();
  return <yFile>file;

  function advance(n = 1): Uint8Array {
    if (n > encoded.byteLength) {
      throw new Error("yenc reached EOF");
    }
    const ret = encoded.subarray(0, n);
    encoded = encoded.subarray(n);
    return ret;
  }
  function advanceTo(
    predicate: (c: number) => boolean,
    excludeDelim = false
  ): Uint8Array {
    const n = encoded.findIndex(predicate);
    if (n < 0) {
      return advance(encoded.byteLength);
    }
    if (excludeDelim) {
      return advance(n);
    }
    return advance(n + 1);
  }
  function readHeader() {
    let atEOL = false;
    while (true) {
      if (s === State.Start) {
        if (!bytes.hasPrefix(encoded, ybegin)) {
          // there are data before the =ybegin keyword
          const i = encoded.findIndex(matchCRLF);
          if (i < 0) {
            throw new Error("yenc begin marker not found");
          }
          advance(i + 1);
          continue;
        }
        advance(ybegin.length);
        while (!atEOL) {
          const arg = readArgument((key) => key === "name");
          const { key, value } = arg;
          atEOL = arg.atEOL;
          switch (key) {
            case "line": {
              file.line = parseInt(value);
              break;
            }
            case "size": {
              file.size = parseInt(value);
              break;
            }
            case "part": {
              file.part = parseInt(value);
              break;
            }
            case "total": {
              file.total = parseInt(value);
              break;
            }
            case "name": {
              file.name = value.trim();
              break;
            }
          }
        }
        if (file.line == null) {
          throw new Error("yenc missing line value");
        }
        if (file.size == null) {
          throw new Error("yenc missing size value");
        }
        s = State.Begin;
      } else if (s === State.Begin) {
        advanceTo(matchNotCRLF, true);
        if (bytes.hasPrefix(encoded, ypart)) {
          // multipart detected
          consumePart();
        } else if (bytes.hasPrefix(encoded, yend)) {
          // empty file
          file.data = new Uint8Array();
          consumeEnd();
        }
        break;
      }
    }
    if (file.part != null || file.total != null) {
      // multipart checks
      if (file.part == null) {
        throw new Error("yenc missing =part line for multipart");
      }
    }
  }
  function consumePart() {
    let atEOL = false;
    advance(ypart.length);
    while (!atEOL) {
      const arg = readArgument();
      const { key, value } = arg;
      atEOL = arg.atEOL;
      if (key === "begin") {
        file.begin = parseInt(value);
        if (file.begin < 1) {
          throw new Error(
            "yenc part begin raw value should start from 1 but got " + value
          );
        }
      } else if (key === "end") {
        file.end = parseInt(value);
      }
    }
    if (file.begin == null) {
      throw new Error("yenc no part begin value");
    }
    file.begin--; // our contract is keep Begin a 0-based index
    if (file.end == null) {
      throw new Error("yenc missing end value for multipart");
    }
    if (file.end < file.begin) {
      throw new Error(
        `yenc part ends ${file.end} before part begin ${file.begin}`
      );
    }
    if (file.end > file.size) {
      throw new Error(
        `yenc part end ${file.end} exceeds file size ${file.size}`
      );
    }
  }
  function readArgument(readToEOL?: (key: string) => boolean): yarg {
    let atEOL = false;
    advanceTo(matchNotEQCRLF, true);
    let token = advanceTo(matchEQCRLF);
    const key = bytes.toString(token.subarray(0, -1));
    if (readToEOL && readToEOL(key)) {
      token = advanceTo(matchCRLF);
    } else {
      token = advanceTo(matchSPCRLF);
    }
    const value = bytes.toString(token.subarray(0, -1));
    if (matchCRLF(token[token.byteLength - 1])) {
      atEOL = true;
    }
    return { key, value, atEOL };
  }
  function decodeData() {
    const b = new ByteWriter();
    while (encoded.byteLength > 0) {
      advanceTo(matchNotCRLF, true);
      if (s === State.Begin && bytes.hasPrefix(encoded, yend)) {
        file.data = b.finish();
        consumeEnd();
        return;
      }
      if (s === State.Begin || s === State.Data) {
        const token = advanceTo(matchEQCRLF);
        if (token.byteLength > 0) {
          for (let i = 0; i < token.byteLength - 1; i++) {
            b.writeByte(byteAdd(token[i], -42));
          }
          if (token[token.byteLength - 1] === 0x3d) {
            s = State.Escape;
            continue;
          } else if (matchCRLF(token[token.byteLength - 1])) {
            s = State.Begin;
            continue;
          }
        }
        break;
      } else if (s === State.Escape) {
        b.writeByte(byteAdd(encoded[0], -106));
        advance(1);
        s = State.Data;
      }
    }
    throw new Error("yenc unexpected EOF");
  }
  function consumeEnd() {
    let atEOL = false;
    let hasSize = false;
    advance(yend.length);
    const realCrc32 = crc32.buf(file.data, crc32seed) >>> 0;
    while (!atEOL) {
      const arg = readArgument();
      const { key, value } = arg;
      atEOL = arg.atEOL;
      if (key === "size") {
        const size = parseInt(value);
        const sizeFromHeader =
          file.part != null ? file.end - file.begin : file.size;
        if (size != sizeFromHeader) {
          throw new Error(
            `yenc header size ${sizeFromHeader} != trailer size ${size}`
          );
        }
        if (size != file.data.byteLength) {
          throw new Error(
            `yenc metadata has size ${size} bute decoded data has szie ${file.data.byteLength}`
          );
        }
        hasSize = true;
      } else if (key === "total") {
        const total = parseInt(value);
        if (total != file.total) {
          throw new Error(
            `yenc header total ${file.total} != trailer total ${total}`
          );
        }
      } else if (key === "pcrc32") {
        const trailerPCrc32 = parseInt(value, 16);
        if (trailerPCrc32 != realCrc32) {
          throw new Error(
            `yenc expect preceeding data to have CRC32 value ${trailerPCrc32.toString(
              16
            )} but got ${realCrc32.toString(16)}`
          );
        }
      } else if (key === "crc32") {
        const trailerCrc32 = parseInt(value, 16);
        if (file.data.byteLength === file.size && trailerCrc32 != realCrc32) {
          throw new Error(
            `yenc expect final file to have CRC32 value ${trailerCrc32.toString(
              16
            )} but got ${realCrc32.toString(16)}`
          );
        }
      }
    }
    if (!hasSize) {
      throw new Error("yenc has no trailer size value");
    }
  }
}

export function encode(file: yFile): Uint8Array {
  if (
    !file.name ||
    file.size == null ||
    file.data == null ||
    ((file.total != null ||
      file.part != null ||
      file.begin != null ||
      file.end != null) &&
      (file.total == null ||
        file.part == null ||
        file.begin == null ||
        file.end == null))
  ) {
    throw new Error("yenc invalid encode parameters");
  }
  if (!file.line) {
    file.line = LineMax;
  }
  if (file.total == null) {
    file.begin = 0;
    file.end = file.size;
  }
  const b = new ByteWriter();
  writeHeader();
  writeBody();
  writeTrailer();
  return b.finish();

  function writeHeader() {
    if (file.total != null) {
      b.write(bytes.fromString(`=ybegin part=${file.part} total=${file.total} `+
      `line=${file.line} size=${file.size} name=${file.name}\n`+
      `=ypart begin=${file.begin + 1} end=${file.end}\n`));
    } else {
      b.write(bytes.fromString(`=ybegin line=${file.line} size=${file.size} name=${file.name}\n`));
    }
  }
  function writeBody() {
    let lineOffset = 0;
    for (let i = 0; i < file.data.byteLength; i++) {
      let c = byteAdd(file.data[i], 42);
      if (matchCriticalChar(c)) {
        c = byteAdd(c, 64);
        if (lineOffset+2 >= file.line) {
          b.writeByte(0x0A);
          lineOffset = 0;
        }
        b.writeByte(0x3D);
        b.writeByte(c);
        lineOffset += 2;
      } else {
        if (lineOffset >= file.line) {
          b.writeByte(0x0A);
          lineOffset = 0;
        }
        b.writeByte(c);
        lineOffset++;
      }
    }
    if (lineOffset > 0) {
      b.writeByte(0x0A);
    }
  }
  function writeTrailer() {
    const sum = (crc32.buf(file.data) >>> 0).toString(16).padStart(8, '0');
    b.write(bytes.fromString(`=yend size=${file.data.byteLength}`));
    if (file.total != null && file.part < file.total) {
      b.write(bytes.fromString(` pcrc32=${sum}`));
    }
    if (file.part === file.total) {
      b.write(bytes.fromString(` crc32=${sum}`));
    }
    b.writeByte(0x0A);
  }
}

function byteAdd(byte: number, offset: number): number {
  return ((byte + offset) >>> 0) % 256;
}

interface yarg {
  key: string;
  value: string;
  atEOL: boolean;
}

enum State {
  Start,
  Begin,
  Escape,
  Data,
}

const ybegin = "=ybegin ";
const ypart = "=ypart ";
const yend = "=yend ";

function matchCRLF(c: number): boolean {
  return c === 0x0d || c === 0x0a;
}

function matchEQCRLF(c: number): boolean {
  return c === 0x3d || c === 0x0d || c === 0x0a;
}

function matchSPCRLF(c: number): boolean {
  return c === 0x20 || c === 0x0d || c === 0x0a;
}

function matchNotCRLF(c: number): boolean {
  return c !== 0x0d && c !== 0x0a;
}

function matchNotEQCRLF(c: number): boolean {
  return c !== 0x3d && c !== 0x0d && c !== 0x0a;
}

function matchCriticalChar(c: number): boolean {
  return c === 0x00 || c === 0x3d || c === 0x0d || c === 0x0a;
}
