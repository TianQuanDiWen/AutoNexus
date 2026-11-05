$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

go run tools/generate_app_icon.go
if ($LASTEXITCODE -ne 0) { throw '生成应用图标失败' }
foreach ($arch in @('amd64', 'arm64', '386')) {
    go run github.com/akavel/rsrc@v0.10.2 `
        -ico internal/server/web/app-icon.ico `
        -o "cmd/autonexus/rsrc_windows_$arch.syso" `
        -arch $arch
    if ($LASTEXITCODE -ne 0) { throw "生成 Windows $arch 图标资源失败" }
}
