export function hasPrefix(bytes: Uint8Array, prefix: string, offset = 0): boolean {
    if (offset > 0) {
        bytes = bytes.subarray(offset);
    }
    if (bytes.byteLength < prefix.length) {
        return false;
    }
    bytes = bytes.subarray(0, prefix.length);
    return bytes.every((c, i) => c === prefix.charCodeAt(i));
}

export function fromString(str: string): Uint8Array {
    return new TextEncoder().encode(str);
}

export function toString(bytes: Uint8Array): string {
    return new TextDecoder().decode(bytes);
}