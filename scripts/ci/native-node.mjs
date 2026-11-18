// Go consumers need Node 24, but do not need npm/yarn discovery or installation.
// Use the same hosted cache as setup-node and verify the executable itself.
import {execFileSync} from 'node:child_process';
import {readdir, appendFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

export async function selectNativeNode({cache, arch, platform=process.platform, githubPath}) {
  if (!cache || !githubPath || !['x64','arm64'].includes(arch) || !['linux','win32'].includes(platform)) throw new Error('native Node cache context required');
  const root=path.join(cache,'node');
  const versions=(await readdir(root)).filter(v=>/^24\.\d+\.\d+$/.test(v)).sort((a,b)=>{
    const x=a.split('.').map(Number),y=b.split('.').map(Number);
    return y[1]-x[1] || y[2]-x[2];
  });
  for (const version of versions) {
    const bin=path.join(root,version,arch,...(platform==='linux'?['bin']:[]));
    const executable=path.join(bin,platform==='win32'?'node.exe':'node');
    let actual;
    try { actual=JSON.parse(execFileSync(executable,['-p','JSON.stringify({version:process.version,arch:process.arch,platform:process.platform})'],{encoding:'utf8',timeout:10000})); }
    catch (error) { if(error.code==='ENOENT')continue;throw error; }
    if (actual.version!==`v${version}` || actual.arch!==arch || actual.platform!==platform) throw new Error('cached Node identity mismatch');
    await appendFile(githubPath,bin+'\n');
    console.log(`native Node ${actual.version} ${platform}-${arch}: ${executable}`);
    return executable;
  }
  throw new Error('native Node 24 missing from hosted cache');
}

if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
  const arch={X64:'x64',ARM64:'arm64'}[process.env.RUNNER_ARCH];
  await selectNativeNode({cache:process.env.RUNNER_TOOL_CACHE,arch,githubPath:process.env.GITHUB_PATH});
}
