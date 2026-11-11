# Third-party notices

HERMES itself contains no third-party code in this repository. The browser
assets below are **not committed**; `sh scripts/fetch-assets.sh` (and the
`assets` stage of the Dockerfile) downloads them into `web/static/vendor/` and
checks each file against a pinned sha256. They are served unchanged from
`/vendor/...`.

| Component | Version | Files (under `web/static/vendor/`) | License | Upstream |
|---|---|---|---|---|
| ONNX Runtime Web | 1.30.0 | `ort-1.30.0/ort.wasm.min.js`, `ort-1.30.0/ort-wasm-simd-threaded.mjs`, `ort-1.30.0/ort-wasm-simd-threaded.wasm` | MIT | https://github.com/microsoft/onnxruntime (npm `onnxruntime-web`) |
| 3D-Speaker CAM++ speaker model (`speech_campplus_sv_zh_en_16k-common_advanced`, ONNX export by sherpa-onnx) | sha256 `aa3cfc16…` | `models/campplus-zh-en-adv-16k.onnx` (renamed on download) | Apache-2.0 | https://github.com/modelscope/3D-Speaker, export: https://github.com/k2-fsa/sherpa-onnx/releases/tag/speaker-recongition-models |
| PDF.js (`pdfjs-dist`) | 4.10.38 | `pdfjs-4.10.38/pdf.min.mjs`, `pdfjs-4.10.38/pdf.worker.min.mjs` | Apache-2.0 | https://github.com/mozilla/pdf.js |
| Readability.js (`@mozilla/readability`) | 0.6.0 | `readability-0.6.0/Readability.js` | Apache-2.0 | https://github.com/mozilla/readability |

Copyright lines:

- ONNX Runtime: Copyright (c) Microsoft Corporation.
- 3D-Speaker models: Copyright the 3D-Speaker authors (modelscope/3D-Speaker), released under the Apache License 2.0. sherpa-onnx (the ONNX export): Copyright the k2-fsa / Next-gen Kaldi authors, Apache License 2.0.
- PDF.js: Copyright Mozilla Foundation.
- Readability.js: Copyright (c) 2010 Arc90 Inc, and the Mozilla contributors.

The speaker front end in `web/static/voiceid-core.js` (log-mel filterbank with
Kaldi conventions) is an independent implementation written for HERMES; it
follows the published behaviour of kaldi-native-fbank (Apache-2.0,
https://github.com/csukuangfj/kaldi-native-fbank) but contains no code from it.

## Apache License 2.0

PDF.js, Readability.js, the 3D-Speaker model and sherpa-onnx are licensed
under the Apache License, Version 2.0. You may obtain a copy of the License at
https://www.apache.org/licenses/LICENSE-2.0. Unless required by applicable law
or agreed to in writing, software distributed under the License is distributed
on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
express or implied. The full license text is also shipped inside each
downloaded file's header (PDF.js, Readability.js).

## ONNX Runtime (MIT License)

Source: https://raw.githubusercontent.com/microsoft/onnxruntime/v1.30.0/LICENSE

```
MIT License

Copyright (c) Microsoft Corporation

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
