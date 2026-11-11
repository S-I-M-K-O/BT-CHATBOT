#!/bin/sh
# Downloads the browser-side assets HERMES does not commit (about 44 MB) into
# <static_dir>/vendor/ and checks every file against a pinned sha256:
#   voiceid  onnxruntime-web 1.30.0 (WASM) + the CAM++ zh/en speaker model
#   docs     pdf.js 4.10.38 (PDF text extraction in the browser)
#   link     Readability.js 0.6.0 (article text for "read a link")
# Without them HERMES still runs; those features degrade gracefully.
#
# Usage: sh scripts/fetch-assets.sh [static_dir] [--only voiceid|docs|link]
#   static_dir defaults to ./static. Idempotent: files that already exist
#   with the right hash are skipped. A hash mismatch deletes the download and
#   exits non-zero. Licenses: see THIRD_PARTY_NOTICES.md.
set -eu

usage() {
	echo "usage: sh scripts/fetch-assets.sh [static_dir] [--only voiceid|docs|link]" >&2
	exit 2
}

STATIC=web/static
ONLY=
while [ $# -gt 0 ]; do
	case "$1" in
	--only)
		[ $# -ge 2 ] || usage
		ONLY=$2
		shift 2
		;;
	--only=*)
		ONLY=${1#--only=}
		shift
		;;
	-h | --help) usage ;;
	-*) usage ;;
	*)
		STATIC=$1
		shift
		;;
	esac
done
case "$ONLY" in
"" | voiceid | docs | link) ;;
*) usage ;;
esac

VENDOR=$STATIC/vendor

ORT=1.30.0
ORT_BASE=https://cdn.jsdelivr.net/npm/onnxruntime-web@$ORT/dist
PDFJS=4.10.38
PDFJS_BASE=https://cdn.jsdelivr.net/npm/pdfjs-dist@$PDFJS/build
READABILITY=0.6.0
READABILITY_BASE=https://cdn.jsdelivr.net/npm/@mozilla/readability@$READABILITY
MODEL_URL=https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/3dspeaker_speech_campplus_sv_zh_en_16k-common_advanced.onnx

if command -v curl >/dev/null 2>&1; then
	download() { curl -fsSL --retry 3 --connect-timeout 20 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	download() { wget -qO "$2" "$1"; }
else
	echo "fetch-assets: need curl or wget" >&2
	exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	echo "fetch-assets: need sha256sum or shasum" >&2
	exit 1
fi

size() { wc -c <"$1" | tr -d ' '; }

TOTAL=0
COUNT=0
PART=

cleanup() {
	if [ -n "$PART" ]; then rm -f "$PART"; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# get <group> <url> <path under vendor/> <sha256>
get() {
	if [ -n "$ONLY" ] && [ "$ONLY" != "$1" ]; then return 0; fi
	dest=$VENDOR/$3
	if [ -f "$dest" ] && [ "$(sha "$dest")" = "$4" ]; then
		echo "ok    $3 (cached)"
	else
		mkdir -p "$(dirname "$dest")"
		PART=$dest.part
		rm -f "$PART"
		if ! download "$2" "$PART"; then
			echo "fetch-assets: download failed: $2" >&2
			exit 1
		fi
		got=$(sha "$PART")
		if [ "$got" != "$4" ]; then
			echo "fetch-assets: sha256 mismatch for $2" >&2
			echo "  expected $4" >&2
			echo "  got      $got" >&2
			exit 1
		fi
		chmod 644 "$PART"
		mv -f "$PART" "$dest"
		PART=
		echo "ok    $3"
	fi
	TOTAL=$((TOTAL + $(size "$dest")))
	COUNT=$((COUNT + 1))
}

# Voice ID (served as /vendor/...; the model is renamed on download)
get voiceid "$ORT_BASE/ort.wasm.min.js" "ort-$ORT/ort.wasm.min.js" \
	faece07e84faa9001f5104bbe4e5c70861d06cb2376a0dea1fa0ad33a67adc81
get voiceid "$ORT_BASE/ort-wasm-simd-threaded.mjs" "ort-$ORT/ort-wasm-simd-threaded.mjs" \
	e13f7f94fc51b4ca72b12faeb1ee95f4ace6dfbc8939bc718aabdc0a27c4299b
get voiceid "$ORT_BASE/ort-wasm-simd-threaded.wasm" "ort-$ORT/ort-wasm-simd-threaded.wasm" \
	3398c10d07d229bd91b364548e130e0e51a8e5704b88c7c083ebbeb78842dee2
get voiceid "$MODEL_URL" "models/campplus-zh-en-adv-16k.onnx" \
	aa3cfc16963a10586a9393f5035d6d6b57e98d358b347f80c2a30bf4f00ceba2

# Own documents (PDF text in the browser)
get docs "$PDFJS_BASE/pdf.min.mjs" "pdfjs-$PDFJS/pdf.min.mjs" \
	27fc2a057a00f92a4334ad06e17dbd7259912954e9fb7f76400bcca5fd190a9c
get docs "$PDFJS_BASE/pdf.worker.min.mjs" "pdfjs-$PDFJS/pdf.worker.min.mjs" \
	1baa1844c89c80a5b2797c916e75ab29254be46d8e9cb53cb6364d7aad84be36

# Read a link
get link "$READABILITY_BASE/Readability.js" "readability-$READABILITY/Readability.js" \
	34dcab3d0832d0019f02990eed6b6124e029e8c32b9f0c6f2550544ff8dff174

echo "fetch-assets: $COUNT files, $TOTAL bytes ($((TOTAL / 1048576)) MB) in $VENDOR"
