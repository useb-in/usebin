import * as yenc from './yenc';
import * as bytes from './bytes';
import * as hex from '@stablelib/hex';
import * as nacl from '@stablelib/nacl';
import * as sha256 from '@stablelib/sha256';
import * as base64 from '@stablelib/base64';
import { decodeText } from './text';
import { CodePage } from './charset';

interface FileInfo {
    key?: Uint8Array; // a 32 bytes NaCl secret-box encryption key
    hash?: string; // the SHA256 hash of the encrypted data, truncated to 16 bytes and hex encoded to string
    messageID?: string; // the message ID of an unencrypted file
}

let text = '';
let saveTimer: number = null;
const textbox = <HTMLTextAreaElement>document.getElementById('x');

for (const event of ['blur', 'change', 'input', 'mouseout']) {
    textbox.addEventListener(event, scheduleTrySave);
}

for (const event of ['blur', 'resize']) {
    window.addEventListener(event, scheduleTrySave);
}

async function main() {
    try {
        const file = parseFileURL();
        if (file != null) {
            await fetchAndDisplayFile(file);
        }
    } catch(e) {
        console.error(e);
        window.history.pushState({}, '', window.location.origin);
    }
}

function scheduleTrySave() {
    if (saveTimer != null) {
        clearTimeout(saveTimer);
    }
    saveTimer = setTimeout(handleTrySave, 4000);
}

async function handleTrySave() {
    saveTimer = null;
    if (textbox.value === '' || text === textbox.value) {
        return;
    }
    text = textbox.value;
    const data = bytes.fromString(text);
    const file = await postFile(data);
    const hash = base64.encodeURLSafe(hex.decode(file.hash)).replace(/=+$/, '');
    const secret = base64.encodeURLSafe(file.key).replace(/=+$/, '');
    window.history.pushState({}, '', `${window.location.origin}/${hash}#${secret}`);
}

function parseFileURL(): FileInfo {
    if (window.location.pathname.length < 2) {
        return null;
    }
    if (window.location.hash.length > 1) {
        // encrypted file
        const hash = hex.encode(base64.decodeURLSafe(window.location.pathname.substring(1))).toLowerCase();
        const key = base64.decodeURLSafe(window.location.hash.substring(1));
        return { hash, key };
    } else {
        // plain text file
        const messageID = decodeURIComponent(window.location.pathname.substring(1));
        return { messageID };
    }
}

async function fetchAndDisplayFile(file: FileInfo) {
    text = file.messageID
        ? decodeText(await fetchFile(file.messageID), CodePage.DOS437En)
        : await fetchAndDecryptFile(file);
    textbox.value = text;
}

async function fetchAndDecryptFile({ hash, key }: FileInfo): Promise<string> {
    const messageID = encryptedFileMessageID(hash);
    const encrypted = await fetchFile(messageID);
    const decrypted = nacl.openSecretBox(key, new Uint8Array(24), encrypted);
    return new TextDecoder().decode(decrypted);
}

async function fetchFile(messageID: string): Promise<Uint8Array> {
    const yencoded = new Uint8Array(await (await fetch(fileEndpoint(messageID))).arrayBuffer());
    return yenc.decode(yencoded).data;
}

async function postFile(data: Uint8Array): Promise<FileInfo> {
    let retries = 0;
    // nonce doesn't have to be ramdon as long as the same
    // nonce is not used twice for the same key
    const nonce = new Uint8Array(24);
    while (true) {
        const key = nacl.generateKey();
        const encrypted = nacl.secretBox(key, nonce, data);
        const hash = hex.encode(sha256.hash(encrypted).subarray(0, 16)).toLowerCase();
        const messageID = encryptedFileMessageID(hash);
        const encoded = yEncFileEncode(encrypted);
        const resp = await fetch(fileEndpoint(messageID), {
            body: encoded,
            method: 'POST',
            referrerPolicy: 'no-referrer',
        });
        if (resp.status != 200) {
            if (retries++ < 3) {
                continue;
            }
            throw new Error(`failed to post file: ${resp.status} ${resp.statusText}`);
        }
        return { key, hash };
    }
}

function fileEndpoint(messageID: string): string {
    return `${window.location.origin}/m/${messageID}.csv`;
}

function encryptedFileMessageID(hash: string): string {
    return hash + '@ngPost.com';
}

function yEncFileEncode(data: Uint8Array): Uint8Array {
    const name = randomName(22) + '.rar';
    const line = yenc.LineMax;
    const total = 10 + randomInt(1000);
    const part = 1 + randomInt(total);
    const size = data.byteLength * total - randomInt(data.byteLength);
    const begin = randomInt(size - data.byteLength);
    const end = begin + data.byteLength;
    return yenc.encode({ name, line, total, part, size, begin, end, data });
}

function randomName(length: number): string {
    let name = '';
    const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
    for (let i = 0; i < length; i++) {
        name += chars[randomInt(chars.length)];
    }
    return name;
}

function randomInt(max: number): number {
    return Math.floor(Math.random() * max);
}

main();
