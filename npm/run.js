#!/usr/bin/env node
// Hand straight over to the platform binary.
//
// execFileSync with stdio inherited, so llmsh owns the terminal: colours, a
// prompt during `llmsh login`, Ctrl-C, and an exit code that is the real one.
// A wrapper that buffers output would turn an interactive command into a log.
const { execFileSync } = require("node:child_process");

const pkg = `@llmskillhub/llmsh-${process.platform}-${process.arch}`;
const bin = process.platform === "win32" ? "llmsh.exe" : "llmsh";

let path;
try {
  path = require.resolve(`${pkg}/bin/${bin}`);
} catch {
  // npm skips an optionalDependency it cannot install, so the failure shows up
  // here rather than at install time. Say which package and why.
  console.error(
    `llmsh: no binary for ${process.platform}/${process.arch}.\n` +
      `  Expected ${pkg}, which npm did not install.\n` +
      `  Builds: https://github.com/llmskillhub/cli/releases`,
  );
  process.exit(1);
}

try {
  execFileSync(path, process.argv.slice(2), { stdio: "inherit" });
} catch (e) {
  // The binary's own exit code, not the wrapper's opinion of it.
  process.exit(typeof e.status === "number" ? e.status : 1);
}
