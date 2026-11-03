# The Anki MCP (@ankimcp/anki-mcp-server), from its published npm package, with its runtime
# dependencies pinned by anki-mcp/package-lock.json, so nothing is fetched when the server runs it.
# anki-mcp/package.json is the package's own without its dev dependencies, with the MCP SDK and
# @hono/node-server moved to versions without known advisories (`npm audit`).
# To update: change version and hash, copy the new package.json into anki-mcp/ (dropping
# devDependencies and the prepare/prebuild scripts), run
# `npm install --package-lock-only --ignore-scripts --omit=dev` there, set npmDepsHash to "",
# build, and copy the hash Nix prints.
{ buildNpmPackage, fetchurl, nodejs_22 }:

buildNpmPackage rec {
  pname = "anki-mcp-server";
  version = "0.27.0";
  nodejs = nodejs_22; # it needs Node 22.12+

  src = fetchurl {
    url = "https://registry.npmjs.org/@ankimcp/anki-mcp-server/-/anki-mcp-server-${version}.tgz";
    hash = "sha512-IBXSPDcNBcGsBZ2RBHzUqJwXE4tDUATQgVWcGU4KVBChKOFwXp4rnTM/nvrG0Ow27Kz2OZHfveDA1X0p06anjw==";
  };
  postPatch = ''
    cp ${./anki-mcp/package.json} package.json
    cp ${./anki-mcp/package-lock.json} package-lock.json
  '';
  npmDepsHash = "sha256-P/7scS8/H85rA9NyE7OhEqPpFWFFKVomuwLK7OLuPlo=";

  # The package is already built; nothing it or its dependencies ship runs at install time.
  dontNpmBuild = true;
  npmFlags = [ "--ignore-scripts" ];
}
