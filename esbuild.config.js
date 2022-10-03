import * as esbuild from 'esbuild';

esbuild.build({
    entryPoints: ['./frontend/usebin.ts'],
    outfile: './static/assets/usebin.js',
    bundle: true,
    minify: true,
    sourcemap: false,
});
