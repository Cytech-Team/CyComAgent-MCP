[CmdletBinding()]
param(
    [ValidateSet('127.0.0.1', 'localhost', '::1')]
    [string]$BindAddress = '127.0.0.1',
    [ValidateRange(1024, 65535)]
    [int]$Port = 7332,
    [string]$TokenFile = (Join-Path $env:ProgramData 'CyComAgent\WindowsBridge\bridge.token'),
    [string]$LogFile = (Join-Path $env:ProgramData 'CyComAgent\WindowsBridge\bridge.log')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:MaxBodyBytes = 1048576
$script:MaxOutputChars = 1048576
$script:StartedAt = Get-Date
$script:Token = $null

function Ensure-ParentDirectory {
    param([Parameter(Mandatory)][string]$Path)

    $parent = [System.IO.Path]::GetDirectoryName($Path)
    if (-not [string]::IsNullOrWhiteSpace($parent) -and -not (Test-Path -LiteralPath $parent -PathType Container)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
}

function New-BridgeToken {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($bytes)
    } finally {
        $rng.Dispose()
    }
    return ([Convert]::ToBase64String($bytes)).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Read-BridgeToken {
    Ensure-ParentDirectory -Path $TokenFile
    if (-not (Test-Path -LiteralPath $TokenFile -PathType Leaf)) {
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($TokenFile, (New-BridgeToken), $utf8)
    }
    $token = ([System.IO.File]::ReadAllText($TokenFile)).Trim()
    if ([string]::IsNullOrWhiteSpace($token)) {
        throw "Bridge token file is empty: $TokenFile"
    }
    return $token
}

function Write-BridgeLog {
    param(
        [Parameter(Mandatory)][string]$Message,
        [ValidateSet('INFO', 'WARN', 'ERROR')][string]$Level = 'INFO'
    )

    try {
        Ensure-ParentDirectory -Path $LogFile
        $line = '{0} [{1}] {2}' -f (Get-Date).ToString('o'), $Level, $Message
        Add-Content -LiteralPath $LogFile -Value $line -Encoding UTF8
    } catch {
        # Logging must not take down the control endpoint.
    }
}

function Get-JsonValue {
    param(
        [AllowNull()][object]$Object,
        [Parameter(Mandatory)][string]$Name,
        [AllowNull()][object]$Default = $null
    )

    if ($null -eq $Object) {
        return $Default
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $Default
    }
    return $property.Value
}

function Limit-Text {
    param(
        [AllowNull()][string]$Text,
        [int]$Limit = $script:MaxOutputChars
    )

    if ($null -eq $Text) {
        return ''
    }
    if ($Text.Length -le $Limit) {
        return $Text
    }
    return $Text.Substring(0, $Limit) + "`r`n...[truncated]"
}

function Test-BridgeAuth {
    param([Parameter(Mandatory)][System.Net.HttpListenerContext]$Context)

    $provided = $Context.Request.Headers['X-CyCom-Token']
    if ([string]::IsNullOrWhiteSpace($provided)) {
        $authorization = $Context.Request.Headers['Authorization']
        if (-not [string]::IsNullOrWhiteSpace($authorization) -and $authorization -match '^Bearer\s+(.+)$') {
            $provided = $Matches[1]
        }
    }
    if ([string]::IsNullOrWhiteSpace($provided)) {
        return $false
    }
    return [string]::Equals($provided.Trim(), $script:Token, [System.StringComparison]::Ordinal)
}

function Write-JsonResponse {
    param(
        [Parameter(Mandatory)][System.Net.HttpListenerContext]$Context,
        [Parameter(Mandatory)][int]$StatusCode,
        [AllowNull()][object]$Payload,
        [string]$SessionId = ''
    )

    $json = if ($null -eq $Payload) { '' } else { $Payload | ConvertTo-Json -Depth 16 -Compress }
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
    $response = $Context.Response
    $response.StatusCode = $StatusCode
    $response.ContentType = 'application/json; charset=utf-8'
    $response.ContentLength64 = $bytes.Length
    $response.Headers['Cache-Control'] = 'no-store'
    if (-not [string]::IsNullOrWhiteSpace($SessionId)) {
        $response.Headers['Mcp-Session-Id'] = $SessionId
    }
    try {
        if ($bytes.Length -gt 0) {
            $response.OutputStream.Write($bytes, 0, $bytes.Length)
        }
    } finally {
        $response.Close()
    }
}

function Write-EmptyResponse {
    param(
        [Parameter(Mandatory)][System.Net.HttpListenerContext]$Context,
        [int]$StatusCode = 204
    )

    $response = $Context.Response
    $response.StatusCode = $StatusCode
    $response.ContentLength64 = 0
    try { } finally { $response.Close() }
}

function Read-RequestJson {
    param([Parameter(Mandatory)][System.Net.HttpListenerContext]$Context)

    if ($Context.Request.ContentLength64 -gt $script:MaxBodyBytes) {
        throw 'Request body is too large.'
    }
    $encoding = $Context.Request.ContentEncoding
    if ($null -eq $encoding) {
        $encoding = [System.Text.Encoding]::UTF8
    }
    $reader = New-Object System.IO.StreamReader($Context.Request.InputStream, $encoding)
    try {
        $body = $reader.ReadToEnd()
    } finally {
        $reader.Dispose()
    }
    if ([string]::IsNullOrWhiteSpace($body)) {
        return $null
    }
    return ($body | ConvertFrom-Json)
}

function Get-IsElevated {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object System.Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-SystemInfo {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object System.Security.Principal.WindowsPrincipal($identity)
    $os = $null
    try { $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop } catch { }
    return [ordered]@{
        ok = $true
        service = 'cycomagent-windows-bridge'
        version = '1.0.0'
        machine = $env:COMPUTERNAME
        user = $identity.Name
        elevated = [bool]$principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
        powershell = $PSVersionTable.PSVersion.ToString()
        os = if ($null -eq $os) { $null } else { $os.Caption }
        os_version = if ($null -eq $os) { $null } else { $os.Version }
        process_id = $PID
        started_at = $script:StartedAt.ToString('o')
        uptime_seconds = [math]::Round(((Get-Date) - $script:StartedAt).TotalSeconds, 3)
        listen = '{0}:{1}' -f $BindAddress, $Port
        mcp_endpoint = 'http://127.0.0.1:{0}/mcp' -f $Port
    }
}

function Invoke-WindowsPowerShell {
    param(
        [Parameter(Mandatory)][string]$Command,
        [string]$WorkingDirectory = (Get-Location).Path,
        [int]$TimeoutSeconds = 300
    )

    if ([string]::IsNullOrWhiteSpace($Command)) {
        throw 'command is required'
    }
    if ($Command.Length -gt 65536) {
        throw 'command exceeds 65536 characters'
    }
    if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 3600) {
        throw 'timeout_seconds must be between 1 and 3600'
    }
    if (-not (Test-Path -LiteralPath $WorkingDirectory -PathType Container)) {
        throw "Working directory does not exist: $WorkingDirectory"
    }

    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $encoded = [Convert]::ToBase64String([System.Text.Encoding]::Unicode.GetBytes($Command))
    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = $powershell
    $startInfo.Arguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ' + $encoded
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true

    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $startInfo
    $started = Get-Date
    [void]$process.Start()
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    $timedOut = -not $process.WaitForExit($TimeoutSeconds * 1000)
    if ($timedOut) {
        try {
            $taskkill = Join-Path $env:SystemRoot 'System32\taskkill.exe'
            & $taskkill /PID $process.Id /T /F | Out-Null
        } catch {
            try { $process.Kill() } catch { }
        }
        [void]$process.WaitForExit(3000)
    }

    $stdout = $stdoutTask.GetAwaiter().GetResult()
    $stderr = $stderrTask.GetAwaiter().GetResult()
    $exitCode = if ($process.HasExited) { $process.ExitCode } else { $null }
    $duration = [math]::Round(((Get-Date) - $started).TotalMilliseconds, 3)
    $process.Dispose()

    return [ordered]@{
        ok = (-not $timedOut -and $exitCode -eq 0)
        exit_code = $exitCode
        timed_out = $timedOut
        duration_ms = $duration
        elevated = [bool](Get-IsElevated)
        stdout = Limit-Text -Text $stdout
        stderr = Limit-Text -Text $stderr
    }
}

function Read-WindowsFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        [int]$MaxBytes = 1048576
    )

    if ($MaxBytes -lt 1 -or $MaxBytes -gt 16777216) {
        throw 'max_bytes must be between 1 and 16777216'
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "File does not exist: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Length -gt $MaxBytes) {
        throw "File is larger than max_bytes: $($item.Length)"
    }
    $bytes = [System.IO.File]::ReadAllBytes($item.FullName)
    $utf8 = New-Object System.Text.UTF8Encoding($false, $true)
    try {
        $text = $utf8.GetString($bytes)
        if ($text.IndexOf([char]0) -ge 0) { throw 'binary' }
        return [ordered]@{ path = $item.FullName; bytes = $bytes.Length; encoding = 'utf-8'; text = $text }
    } catch {
        return [ordered]@{ path = $item.FullName; bytes = $bytes.Length; encoding = 'base64'; base64 = [Convert]::ToBase64String($bytes) }
    }
}

function Get-WindowsFileList {
    param(
        [string]$Path = (Get-Location).Path,
        [bool]$Recurse = $false,
        [int]$Limit = 5000
    )

    if ($Limit -lt 1 -or $Limit -gt 10000) { throw 'limit must be between 1 and 10000' }
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { throw "Directory does not exist: $Path" }
    $items = if ($Recurse) {
        @(Get-ChildItem -LiteralPath $Path -Force -File -Recurse -ErrorAction Stop | Select-Object -First $Limit)
    } else {
        @(Get-ChildItem -LiteralPath $Path -Force -ErrorAction Stop | Select-Object -First $Limit)
    }
    $entries = @($items | ForEach-Object {
        [ordered]@{
            name = $_.Name
            path = $_.FullName
            type = if ($_.PSIsContainer) { 'directory' } else { 'file' }
            length = if ($_.PSIsContainer) { $null } else { $_.Length }
            last_write_time = $_.LastWriteTime.ToString('o')
            attributes = $_.Attributes.ToString()
        }
    })
    return [ordered]@{ path = (Get-Item -LiteralPath $Path -Force).FullName; count = $entries.Count; entries = $entries; truncated = ($items.Count -ge $Limit) }
}

function Get-WindowsProcesses {
    param([int]$Limit = 500)

    if ($Limit -lt 1 -or $Limit -gt 2000) { throw 'limit must be between 1 and 2000' }
    $processes = @(Get-Process | Sort-Object ProcessName, Id | Select-Object -First $Limit)
    $entries = @($processes | ForEach-Object {
        [ordered]@{
            name = $_.ProcessName
            id = $_.Id
            responding = if ($null -eq $_.Responding) { $null } else { [bool]$_.Responding }
            session_id = $_.SessionId
            path = try { $_.Path } catch { $null }
        }
    })
    return [ordered]@{ count = $entries.Count; processes = $entries; truncated = ($processes.Count -ge $Limit) }
}

function Invoke-WindowsServiceAction {
    param(
        [ValidateSet('list', 'start', 'stop', 'restart', 'status')][string]$Action = 'list',
        [string]$Name = '',
        [int]$Limit = 500
    )

    if ($Action -eq 'list') {
        $services = @(Get-Service | Sort-Object Name | Select-Object -First $Limit)
        $entries = @($services | ForEach-Object { [ordered]@{ name = $_.Name; display_name = $_.DisplayName; status = $_.Status.ToString(); start_type = $_.StartType.ToString() } })
        return [ordered]@{ action = 'list'; count = $entries.Count; services = $entries; truncated = ($services.Count -ge $Limit) }
    }
    if ([string]::IsNullOrWhiteSpace($Name)) { throw 'name is required for service actions' }
    $service = Get-Service -Name $Name -ErrorAction Stop
    switch ($Action) {
        'start' { Start-Service -Name $Name -ErrorAction Stop }
        'stop' { Stop-Service -Name $Name -Force -ErrorAction Stop }
        'restart' { Restart-Service -Name $Name -Force -ErrorAction Stop }
        'status' { }
    }
    $service = Get-Service -Name $Name -ErrorAction Stop
    return [ordered]@{ action = $Action; name = $service.Name; display_name = $service.DisplayName; status = $service.Status.ToString(); start_type = $service.StartType.ToString() }
}

function New-McpTool {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Description,
        [Parameter(Mandatory)][object]$Properties,
        [string[]]$Required = @()
    )
    return [ordered]@{
        name = $Name
        description = $Description
        inputSchema = [ordered]@{ type = 'object'; properties = $Properties; required = $Required }
    }
}

function Get-McpTools {
    $execProperties = [ordered]@{
        command = [ordered]@{ type = 'string'; description = 'PowerShell command to run on this Windows machine.' }
        cwd = [ordered]@{ type = 'string'; description = 'Optional existing Windows working directory.' }
        timeout_seconds = [ordered]@{ type = 'integer'; minimum = 1; maximum = 3600; default = 300 }
    }
    $fileReadProperties = [ordered]@{
        path = [ordered]@{ type = 'string'; description = 'Absolute or relative Windows file path.' }
        max_bytes = [ordered]@{ type = 'integer'; minimum = 1; maximum = 16777216; default = 1048576 }
    }
    $fileListProperties = [ordered]@{
        path = [ordered]@{ type = 'string'; description = 'Directory to list.' }
        recurse = [ordered]@{ type = 'boolean'; default = $false }
        limit = [ordered]@{ type = 'integer'; minimum = 1; maximum = 10000; default = 5000 }
    }
    $limitProperties = [ordered]@{ limit = [ordered]@{ type = 'integer'; minimum = 1; maximum = 2000; default = 500 } }
    $serviceProperties = [ordered]@{
        action = [ordered]@{ type = 'string'; enum = @('list', 'start', 'stop', 'restart', 'status'); default = 'list' }
        name = [ordered]@{ type = 'string' }
        limit = [ordered]@{ type = 'integer'; minimum = 1; maximum = 2000; default = 500 }
    }
    return @(
        (New-McpTool -Name 'windows_system_info' -Description 'Inspect this Windows host, current identity, OS, PowerShell and elevation state.' -Properties ([ordered]@{})),
        (New-McpTool -Name 'windows_exec' -Description 'Execute a PowerShell command on this Windows host. The bridge process runs with the Windows task token; elevated status is reported in the result.' -Properties $execProperties -Required @('command')),
        (New-McpTool -Name 'windows_file_read' -Description 'Read a local Windows file as UTF-8 text or base64 for binary content.' -Properties $fileReadProperties -Required @('path')),
        (New-McpTool -Name 'windows_file_list' -Description 'List files and directories on the local Windows host.' -Properties $fileListProperties),
        (New-McpTool -Name 'windows_processes' -Description 'List local Windows processes.' -Properties $limitProperties),
        (New-McpTool -Name 'windows_services' -Description 'List or control local Windows services.' -Properties $serviceProperties)
    )
}

function Invoke-McpTool {
    param(
        [Parameter(Mandatory)][string]$Name,
        [AllowNull()][object]$Arguments
    )

    switch ($Name) {
        'windows_system_info' { return (Get-SystemInfo) }
        'windows_exec' {
            $command = [string](Get-JsonValue -Object $Arguments -Name 'command' -Default '')
            $cwd = [string](Get-JsonValue -Object $Arguments -Name 'cwd' -Default (Get-Location).Path)
            $timeout = [int](Get-JsonValue -Object $Arguments -Name 'timeout_seconds' -Default 300)
            return (Invoke-WindowsPowerShell -Command $command -WorkingDirectory $cwd -TimeoutSeconds $timeout)
        }
        'windows_file_read' {
            $path = [string](Get-JsonValue -Object $Arguments -Name 'path' -Default '')
            $maxBytes = [int](Get-JsonValue -Object $Arguments -Name 'max_bytes' -Default 1048576)
            return (Read-WindowsFile -Path $path -MaxBytes $maxBytes)
        }
        'windows_file_list' {
            $path = [string](Get-JsonValue -Object $Arguments -Name 'path' -Default (Get-Location).Path)
            $recurse = [bool](Get-JsonValue -Object $Arguments -Name 'recurse' -Default $false)
            $limit = [int](Get-JsonValue -Object $Arguments -Name 'limit' -Default 5000)
            return (Get-WindowsFileList -Path $path -Recurse $recurse -Limit $limit)
        }
        'windows_processes' {
            $limit = [int](Get-JsonValue -Object $Arguments -Name 'limit' -Default 500)
            return (Get-WindowsProcesses -Limit $limit)
        }
        'windows_services' {
            $action = [string](Get-JsonValue -Object $Arguments -Name 'action' -Default 'list')
            $name = [string](Get-JsonValue -Object $Arguments -Name 'name' -Default '')
            $limit = [int](Get-JsonValue -Object $Arguments -Name 'limit' -Default 500)
            return (Invoke-WindowsServiceAction -Action $action -Name $name -Limit $limit)
        }
        default { throw "Unknown MCP tool: $Name" }
    }
}

function Handle-McpRequest {
    param(
        [Parameter(Mandatory)][System.Net.HttpListenerContext]$Context,
        [Parameter(Mandatory)][object]$Request
    )

    $method = [string](Get-JsonValue -Object $Request -Name 'method' -Default '')
    $id = Get-JsonValue -Object $Request -Name 'id' -Default $null
    if ($method -like 'notifications/*') {
        Write-EmptyResponse -Context $Context -StatusCode 202
        return
    }

    switch ($method) {
        'initialize' {
            $sessionId = [Guid]::NewGuid().ToString('N')
            $result = [ordered]@{
                protocolVersion = '2025-06-18'
                capabilities = [ordered]@{ tools = [ordered]@{ listChanged = $false } }
                serverInfo = [ordered]@{ name = 'cycomagent-windows-bridge'; version = '1.0.0' }
                instructions = 'Windows-native local bridge. Endpoint is loopback-only and requires X-CyCom-Token or Authorization: Bearer.'
            }
            Write-JsonResponse -Context $Context -StatusCode 200 -Payload ([ordered]@{ jsonrpc = '2.0'; id = $id; result = $result }) -SessionId $sessionId
            return
        }
        'tools/list' {
            Write-JsonResponse -Context $Context -StatusCode 200 -Payload ([ordered]@{ jsonrpc = '2.0'; id = $id; result = [ordered]@{ tools = @(Get-McpTools) } })
            return
        }
        'tools/call' {
            $params = Get-JsonValue -Object $Request -Name 'params' -Default $null
            $name = [string](Get-JsonValue -Object $params -Name 'name' -Default '')
            $arguments = Get-JsonValue -Object $params -Name 'arguments' -Default $null
            try {
                $data = Invoke-McpTool -Name $name -Arguments $arguments
                $text = $data | ConvertTo-Json -Depth 16 -Compress
                $result = [ordered]@{
                    content = @([ordered]@{ type = 'text'; text = $text })
                    structuredContent = $data
                    isError = $false
                }
                Write-JsonResponse -Context $Context -StatusCode 200 -Payload ([ordered]@{ jsonrpc = '2.0'; id = $id; result = $result })
            } catch {
                $message = $_.Exception.Message
                Write-BridgeLog -Level 'ERROR' -Message ('MCP tool {0}: {1}' -f $name, $message)
                $result = [ordered]@{ content = @([ordered]@{ type = 'text'; text = $message }); isError = $true }
                Write-JsonResponse -Context $Context -StatusCode 200 -Payload ([ordered]@{ jsonrpc = '2.0'; id = $id; result = $result })
            }
            return
        }
        default {
            Write-JsonResponse -Context $Context -StatusCode 200 -Payload ([ordered]@{ jsonrpc = '2.0'; id = $id; error = [ordered]@{ code = -32601; message = ('Method not found: {0}' -f $method) } })
            return
        }
    }
}

function Handle-Request {
    param([Parameter(Mandatory)][System.Net.HttpListenerContext]$Context)

    $path = $Context.Request.Url.AbsolutePath
    $method = $Context.Request.HttpMethod.ToUpperInvariant()
    $started = Get-Date
    try {
        if ($method -eq 'OPTIONS') {
            Write-EmptyResponse -Context $Context -StatusCode 204
            return
        }
        if ($path -eq '/health' -and $method -eq 'GET') {
            Write-JsonResponse -Context $Context -StatusCode 200 -Payload (Get-SystemInfo)
            return
        }
        if ($path -like '/.well-known/*' -and $method -eq 'GET') {
            Write-EmptyResponse -Context $Context -StatusCode 404
            return
        }
        if (-not (Test-BridgeAuth -Context $Context)) {
            Write-JsonResponse -Context $Context -StatusCode 401 -Payload ([ordered]@{ ok = $false; error = 'unauthorized' })
            return
        }
        if ($path -eq '/capabilities' -and $method -eq 'GET') {
            Write-JsonResponse -Context $Context -StatusCode 200 -Payload ([ordered]@{ ok = $true; mcp = 'http://127.0.0.1:{0}/mcp' -f $Port; tools = @(Get-McpTools); system = Get-SystemInfo })
            return
        }
        if ($path -eq '/exec' -and $method -eq 'POST') {
            $request = Read-RequestJson -Context $Context
            $command = [string](Get-JsonValue -Object $request -Name 'command' -Default '')
            $cwd = [string](Get-JsonValue -Object $request -Name 'cwd' -Default (Get-Location).Path)
            $timeout = [int](Get-JsonValue -Object $request -Name 'timeout_seconds' -Default 300)
            Write-JsonResponse -Context $Context -StatusCode 200 -Payload (Invoke-WindowsPowerShell -Command $command -WorkingDirectory $cwd -TimeoutSeconds $timeout)
            return
        }
        if ($path -eq '/mcp' -and $method -eq 'POST') {
            $request = Read-RequestJson -Context $Context
            if ($null -eq $request) { throw 'JSON-RPC request body is required' }
            Handle-McpRequest -Context $Context -Request $request
            return
        }
        Write-JsonResponse -Context $Context -StatusCode 404 -Payload ([ordered]@{ ok = $false; error = 'not_found' })
    } catch {
        $message = $_.Exception.Message
        Write-BridgeLog -Level 'ERROR' -Message ('{0} {1}: {2}' -f $method, $path, $message)
        try { Write-JsonResponse -Context $Context -StatusCode 400 -Payload ([ordered]@{ ok = $false; error = $message }) } catch { }
    } finally {
        $duration = [math]::Round(((Get-Date) - $started).TotalMilliseconds, 3)
        Write-BridgeLog -Message ('{0} {1} {2}ms' -f $method, $path, $duration)
    }
}

if ($BindAddress -eq 'localhost') {
    $BindAddress = '127.0.0.1'
}
$script:Token = Read-BridgeToken
Ensure-ParentDirectory -Path $LogFile

$listener = New-Object System.Net.HttpListener
$prefix = 'http://{0}:{1}/' -f $BindAddress, $Port
$listener.Prefixes.Add($prefix)
try {
    $listener.Start()
} catch {
    Write-BridgeLog -Level 'ERROR' -Message ('Cannot listen on {0}: {1}' -f $prefix, $_.Exception.Message)
    throw
}

Write-BridgeLog -Message ('Started on {0}; elevated={1}; pid={2}' -f $prefix, (Get-IsElevated), $PID)
try {
    while ($listener.IsListening) {
        try {
            $context = $listener.GetContext()
            Handle-Request -Context $context
        } catch [System.Net.HttpListenerException] {
            if ($listener.IsListening) { Write-BridgeLog -Level 'ERROR' -Message $_.Exception.Message }
        } catch {
            if ($listener.IsListening) { Write-BridgeLog -Level 'ERROR' -Message $_.Exception.Message }
        }
    }
} finally {
    if ($listener.IsListening) { $listener.Stop() }
    $listener.Close()
    Write-BridgeLog -Message 'Stopped'
}
