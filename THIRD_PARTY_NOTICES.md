# Third-party notices

## magpie-community Factory plugin

The Factory tool-schema envelope, local-reference relocation and tool-argument
stream adaptation in `internal/factory/` are adapted from
`magpie-community/plugins/packages/factory/anthropic-tools.mjs`, commit
`d24fd5d1824e1078e7c8c56732813f8263fcf728`.
Model routing and explicit Fast Mode mapping also reference this plugin.
The droid client headers and fixed system-prompt opening in
`internal/factory/droid.go` are adapted from the same package's `index.mjs`
(`factoryHeaders`, `send`, `droidBody`, `droidChat`, `anthropicBody`).

Source: https://github.com/magpie-community/plugins/tree/d24fd5d1824e1078e7c8c56732813f8263fcf728/packages/factory

MIT License

Copyright (c) 2026 magpie-community contributors

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
