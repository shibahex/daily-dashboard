# The MyFitnessPal MCP (github.com/shibahex/myfitnesspal-mcp), built from a pinned commit with
# nixpkgs' Python packages, so nothing is fetched from PyPI when the server runs it.
# To update: change rev, set hash to "", build, and copy the hash Nix prints.
{ lib, python3Packages, fetchFromGitHub, fetchPypi, withAutorefresh ? false }:

let
  # The MCP is written for the MCP SDK 1.x (FastMCP); nixpkgs has 2.x.
  mcp1 = python3Packages.mcp.overridePythonAttrs (old: rec {
    version = "1.30.0";
    src = fetchPypi {
      pname = "mcp";
      inherit version;
      hash = "sha256-RFQUYl/OXClfqlBbsRus7OZhq29AKNV8k121eCC3o+Q=";
    };
    dependencies = old.dependencies ++ [ python3Packages.httpx-sse python3Packages.pydantic-settings ];
    patches = [ ];
    postPatch = "";
    doCheck = false;
    pythonImportsCheck = [ "mcp.server.fastmcp" ];
  });
in
python3Packages.buildPythonPackage {
  pname = "mfp-mcp";
  version = "0.4.1";
  pyproject = true;

  src = fetchFromGitHub {
    owner = "shibahex";
    repo = "myfitnesspal-mcp";
    rev = "986929b0868ae4908cf846b2211258bd6cc83248";
    hash = "sha256-ICZ93c0Zi10aCoKR6/FmZpoYjzdYXWByx9/w0FvTGzk=";
  };

  build-system = [ python3Packages.hatchling ];
  dependencies = with python3Packages; [ mcp1 myfitnesspal curl-cffi lxml platformdirs ]
    ++ lib.optional withAutorefresh playwright;
  # nixpkgs' curl-cffi is newer than pyproject.toml's upper bound; the tests check it still works.
  pythonRelaxDeps = [ "curl-cffi" ];

  nativeCheckInputs = [ python3Packages.pytestCheckHook ];
  pythonImportsCheck = [ "myfitnesspal_mcp.server" ];
}
