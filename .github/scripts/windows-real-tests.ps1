Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-TargetedGoTest {
    param(
        [Parameter(Mandatory = $true)][string]$Package,
        [Parameter(Mandatory = $true)][string]$Test,
        [string]$RunExpression = '',
        [string[]]$RequiredSubtests = @()
    )
    if ($RunExpression -eq '') {
        $RunExpression = '^' + $Test + '$'
    }
    $logName = ($Package -replace '[^A-Za-z0-9]+', '-') + '-' + $Test + '.log'
    $log = Join-Path $env:RUNNER_TEMP $logName
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        # Windows PowerShell 5.1 treats Go's normal stderr progress as a terminating error.
        $ErrorActionPreference = 'Continue'
        & go.exe test -mod=readonly -count=1 -v -run $RunExpression $Package 2>&1 | Tee-Object -FilePath $log
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($exitCode -ne 0) {
        throw "$Test failed with exit code $exitCode"
    }
    $skips = @(Select-String -Path $log -Pattern '--- SKIP:')
    if ($skips.Count -ne 0) {
        $skips | ForEach-Object { Write-Host $_.Line }
        throw "$Test reported SKIP; a real fixture run is required"
    }
    $passPattern = '^--- PASS: ' + [regex]::Escape($Test) + '\s'
    if (-not (Select-String -Path $log -Pattern $passPattern -Quiet)) {
        throw "$Test did not report a top-level PASS"
    }
    foreach ($subtest in $RequiredSubtests) {
        $subtestPassPattern = '^    --- PASS: ' + [regex]::Escape($Test + '/' + $subtest) + '\s'
        if (-not (Select-String -Path $log -Pattern $subtestPassPattern -Quiet)) {
            throw "$Test/$subtest did not report PASS"
        }
    }
}

$failure = $null
$cleanupFailures = @()
$userName = $null
$userCreated = $false
$sshServiceName = $null
$sshConfigRoot = $null
$workRoot = $null
$vhdPath = $null
$diskpartCreateScript = $null
$diskpartDetachScript = $null
$winRMServiceCaptured = $false
$winRMListenersCaptured = $false
$winRMStartType = $null
$winRMStatus = $null
$winRMListeners = @()
$openSSHStateCaptured = $false
$openSSHInstallAttempted = $false
$openSSHOriginalState = $null
$capabilityName = 'OpenSSH.Server~~~~0.0.1.0'
$tokenFilterChanged = $false
$tokenFilterExisted = $false
$tokenFilterValue = $null
$tokenFilterPath = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System'

function Register-CleanupFailure {
    param([Parameter(Mandatory = $true)][object]$Cause)
    $message = $Cause.ToString()
    $script:cleanupFailures += $message
    Write-Warning ('Fixture cleanup failed: ' + $message)
}

function Invoke-CleanupStep {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Action
    )
    try {
        & $Action
    } catch {
        Register-CleanupFailure ($Name + ': ' + $_.Exception.Message)
    }
}

try {
    $gitBin = Join-Path $env:ProgramFiles 'Git\bin'
    $system32 = Join-Path $env:WINDIR 'System32'
    $env:PATH = $gitBin + ';' + $system32 + ';' + $env:PATH
    if ((Get-Command bash.exe -CommandType Application | Select-Object -First 1).Source -ne (Join-Path $gitBin 'bash.exe')) {
        throw 'Git Bash must precede any WSL bash on PATH'
    }
    if ((Get-Command whoami.exe -CommandType Application | Select-Object -First 1).Source -ne (Join-Path $system32 'whoami.exe')) {
        throw 'Windows System32 whoami must precede Unix whoami on PATH'
    }

    $dVolume = Get-Volume -DriveLetter D -ErrorAction SilentlyContinue
    if ($null -eq $dVolume) {
        $vhdPath = Join-Path $env:RUNNER_TEMP ('nexterm-second-volume-' + $PID + '.vhd')
        $diskpartCreateScript = Join-Path $env:RUNNER_TEMP ('nexterm-create-volume-' + $PID + '.txt')
        $diskpartDetachScript = Join-Path $env:RUNNER_TEMP ('nexterm-detach-volume-' + $PID + '.txt')
        @(
            'create vdisk file="' + $vhdPath + '" maximum=256 type=expandable',
            'select vdisk file="' + $vhdPath + '"',
            'attach vdisk',
            'create partition primary',
            'format fs=ntfs quick label=NexTermCI',
            'assign letter=D'
        ) | Set-Content -Path $diskpartCreateScript -Encoding ASCII
        & diskpart.exe /s $diskpartCreateScript | Out-Host
        if ($LASTEXITCODE -ne 0) {
            throw "DiskPart failed to create the real D: volume with exit code $LASTEXITCODE"
        }
        $deadline = [DateTime]::UtcNow.AddSeconds(30)
        do {
            $dVolume = Get-Volume -DriveLetter D -ErrorAction SilentlyContinue
            if ($null -ne $dVolume -and (Test-Path 'D:\')) {
                break
            }
            Start-Sleep -Milliseconds 500
        } while ([DateTime]::UtcNow -lt $deadline)
    }
    $cVolume = Get-Volume -DriveLetter C -ErrorAction Stop
    $dVolume = Get-Volume -DriveLetter D -ErrorAction Stop
    if ($cVolume.ObjectId -eq $dVolume.ObjectId) {
        throw 'C: and D: must be distinct volumes'
    }
    if ([string]$dVolume.DriveType -ne 'Fixed') {
        throw "D: must be a fixed volume, not $($dVolume.DriveType)"
    }
    $null = Get-Partition -DriveLetter C -ErrorAction Stop
    $null = Get-Partition -DriveLetter D -ErrorAction Stop
    if (-not (Test-Path 'D:\')) {
        throw 'D: is not mounted'
    }

    $workRoot = 'D:\NexTerm-CI-' + [guid]::NewGuid().ToString('N')
    $null = New-Item -ItemType Directory -Path $workRoot
    $env:NEXTERM_WINDOWS_TEST_SECOND_ROOT = 'D:\'
    $env:NEXTERM_WINDOWS_TEST_WORK_ROOT = $workRoot
    $env:NEXTERM_WINDOWS_LOCAL_INTEGRATION = '1'
    Invoke-TargetedGoTest -Package './internal/fs/local' -Test 'TestRealWindowsSecondVolume'
    Remove-Item Env:NEXTERM_WINDOWS_LOCAL_INTEGRATION

    $userName = 'nxt' + [guid]::NewGuid().ToString('N').Substring(0, 10)
    $password = [guid]::NewGuid().ToString('N') + 'aA1!'
    Write-Host "::add-mask::$password"
    $securePassword = ConvertTo-SecureString $password -AsPlainText -Force
    $null = New-LocalUser -Name $userName -Password $securePassword -AccountNeverExpires -PasswordNeverExpires
    $userCreated = $true
    Add-LocalGroupMember -Group 'Administrators' -Member $userName

    $tokenFilter = Get-ItemProperty -Path $tokenFilterPath -Name 'LocalAccountTokenFilterPolicy' -ErrorAction SilentlyContinue
    $tokenFilterExisted = $null -ne $tokenFilter
    if ($tokenFilterExisted) {
        $tokenFilterValue = $tokenFilter.LocalAccountTokenFilterPolicy
    }
    $null = New-ItemProperty -Path $tokenFilterPath -Name 'LocalAccountTokenFilterPolicy' -PropertyType DWord -Value 1 -Force
    $tokenFilterChanged = $true

    & icacls.exe $workRoot /grant "${userName}:(OI)(CI)F" /Q | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to grant the fixture user access to D: with exit code $LASTEXITCODE"
    }

    $winRMService = Get-Service -Name WinRM
    $winRMStartType = $winRMService.StartType
    $winRMStatus = [string]$winRMService.Status
    $winRMServiceCaptured = $true
    if ($winRMStatus -ne 'Running' -and $winRMStatus -ne 'Stopped') {
        throw "Cannot preserve WinRM service state $winRMStatus"
    }
    Set-Service -Name WinRM -StartupType Manual
    Start-Service WinRM
    $winRMListeners = @(Get-WSManInstance -ResourceURI winrm/config/listener -Enumerate | ForEach-Object {
        [PSCustomObject]@{
            Address = [string]$_.Address
            Transport = [string]$_.Transport
            Port = [string]$_.Port
            Hostname = [string]$_.Hostname
            Enabled = $_.Enabled
            URLPrefix = [string]$_.URLPrefix
            CertificateThumbprint = [string]$_.CertificateThumbprint
        }
    })
    $winRMListenersCaptured = $true
    $listeners = @(Get-WSManInstance -ResourceURI winrm/config/listener -Enumerate)
    foreach ($listener in $listeners) {
        Remove-WSManInstance -ResourceURI winrm/config/listener -SelectorSet @{ Transport = $listener.Transport; Address = $listener.Address }
    }
    $null = New-WSManInstance -ResourceURI winrm/config/listener -SelectorSet @{ Transport = 'HTTP'; Address = 'IP:127.0.0.1' } -ValueSet @{ Port = '5985' }
    $loopbackListeners = @(Get-WSManInstance -ResourceURI winrm/config/listener -Enumerate)
    if ($loopbackListeners.Count -ne 1 -or
        $loopbackListeners[0].Transport -ne 'HTTP' -or
        $loopbackListeners[0].Address -ne 'IP:127.0.0.1' -or
        ([int]$loopbackListeners[0].Port) -ne 5985) {
        throw 'Every WinRM transport must be disabled or restricted to the HTTP loopback listener'
    }
    $env:NEXTERM_WINRM_INTEGRATION = '1'
    $env:NEXTERM_WINRM_HOST = '127.0.0.1'
    $env:NEXTERM_WINRM_PORT = '5985'
    $env:NEXTERM_WINRM_USER = $userName
    $env:NEXTERM_WINRM_PASSWORD = $password
    $env:NEXTERM_WINRM_DOMAIN = $env:COMPUTERNAME
    $env:NEXTERM_WINRM_AUTH = 'ntlm'
    $env:NEXTERM_WINRM_TLS = 'false'
    $env:NEXTERM_WINRM_ACCEPT_INVALID_CERTS = 'false'
    $env:NEXTERM_WINRM_INITIAL_CWD = $workRoot
    Invoke-TargetedGoTest -Package './internal/transport/winrm' -Test 'TestRealWindowsSecondVolume'
    Invoke-TargetedGoTest -Package './internal/transport/winrm' -Test 'TestRealWindowsServer' -RunExpression '^TestRealWindowsServer$/^(cwd_persistence|filesystem|relative_read_write_and_backup_after_cwd_change)$' -RequiredSubtests @('cwd_persistence', 'filesystem', 'relative_read_write_and_backup_after_cwd_change')

    $capability = Get-WindowsCapability -Online -Name $capabilityName
    $openSSHOriginalState = [string]$capability.State
    $openSSHStateCaptured = $true
    if ($capability.State -ne 'Installed') {
        $openSSHInstallAttempted = $true
        $installed = Add-WindowsCapability -Online -Name $capabilityName
        if ($installed.RestartNeeded) {
            throw 'OpenSSH Server installation requires a restart and cannot be used in this runner job'
        }
    }
    $capability = Get-WindowsCapability -Online -Name $capabilityName
    if ($capability.State -ne 'Installed') {
        throw "OpenSSH Server capability state is $($capability.State)"
    }
    $openSSHRoot = Join-Path $env:WINDIR 'System32\OpenSSH'
    $sshd = Join-Path $openSSHRoot 'sshd.exe'
    $sshKeygen = Join-Path $openSSHRoot 'ssh-keygen.exe'
    $sftpServer = Join-Path $openSSHRoot 'sftp-server.exe'
    foreach ($executable in @($sshd, $sshKeygen, $sftpServer)) {
        if (-not (Test-Path $executable)) {
            throw "OpenSSH executable is missing: $executable"
        }
    }

    $sshConfigRoot = Join-Path $env:ProgramData ('ssh\nexterm-ci-' + $PID)
    $null = New-Item -ItemType Directory -Path $sshConfigRoot -Force
    $hostKey = Join-Path $sshConfigRoot 'ssh_host_ed25519_key'
    & $sshKeygen -q -t ed25519 -f $hostKey -N '""'
    if ($LASTEXITCODE -ne 0) {
        throw "ssh-keygen failed with exit code $LASTEXITCODE"
    }
    $currentUserSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    & icacls.exe $hostKey /inheritance:r /remove:g ('*' + $currentUserSid) /grant '*S-1-5-18:F' /grant '*S-1-5-32-544:F' /Q | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to secure the SSH host key with exit code $LASTEXITCODE"
    }
    & icacls.exe $hostKey /setowner '*S-1-5-18' /Q | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to set the SSH host-key owner with exit code $LASTEXITCODE"
    }
    $fingerprintLine = & $sshKeygen -lf ($hostKey + '.pub') -E sha256
    if ($LASTEXITCODE -ne 0) {
        throw "SSH host-key fingerprint failed with exit code $LASTEXITCODE"
    }
    $fingerprint = (($fingerprintLine | Select-Object -First 1) -split '\s+')[1]
    if (-not $fingerprint.StartsWith('SHA256:')) {
        throw 'Could not determine the SSH host-key fingerprint'
    }

    $portListener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $portListener.Start()
    $sshPort = ([System.Net.IPEndPoint]$portListener.LocalEndpoint).Port
    $portListener.Stop()
    $sshConfig = Join-Path $sshConfigRoot 'sshd_config'
    @(
        ('Port ' + $sshPort),
        'ListenAddress 127.0.0.1',
        ('HostKey ' + $hostKey.Replace('\', '/')),
        'PasswordAuthentication yes',
        'PubkeyAuthentication no',
        'PermitEmptyPasswords no',
        ('AllowUsers ' + $userName),
        'UseDNS no',
        ('Subsystem sftp ' + $sftpServer.Replace('\', '/')),
        'LogLevel VERBOSE'
    ) | Set-Content -Path $sshConfig -Encoding ASCII
    $serviceName = 'NexTermSSHD' + $PID
    $binaryPath = '"{0}" -D -f "{1}"' -f $sshd, $sshConfig
    $null = New-Service -Name $serviceName -BinaryPathName $binaryPath -StartupType Manual
    $sshServiceName = $serviceName
    Start-Service $sshServiceName
    $sshReady = $false
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        $sshListeners = @(Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort $sshPort -State Listen -ErrorAction SilentlyContinue)
        if ($sshListeners.Count -ne 0) {
            $sshReady = $true
            break
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $sshReady) {
        throw 'OpenSSH did not bind its loopback port'
    }
    $env:NEXTERM_SSH_WINDOWS_INTEROP = '1'
    $env:NEXTERM_SSH_TEST_HOST = '127.0.0.1:' + $sshPort
    $env:NEXTERM_SSH_TEST_USER = $userName
    $env:NEXTERM_SSH_TEST_PASSWORD = $password
    $env:NEXTERM_SSH_TEST_HOST_KEY = $fingerprint
    Invoke-TargetedGoTest -Package './internal/transport/ssh' -Test 'TestWindowsOpenSSHSecondVolume'
} catch {
    $failure = $_
} finally {
    $sshServiceRemoved = $null -eq $sshServiceName
    if (-not $sshServiceRemoved) {
        Invoke-CleanupStep 'stop temporary sshd service' {
            Stop-Service $sshServiceName -ErrorAction Stop
        }
        Invoke-CleanupStep 'delete temporary sshd service registration' {
            & sc.exe delete $sshServiceName | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "sc.exe delete failed with exit code $LASTEXITCODE"
            }
        }
        $sshServiceRemoved = $false
        Invoke-CleanupStep 'verify temporary sshd service registration removal' {
            $remaining = @(Get-CimInstance -ClassName Win32_Service -Filter "Name='$sshServiceName'" -ErrorAction Stop)
            if ($remaining.Count -ne 0) {
                throw 'Temporary sshd registration remains; preserving its configuration, user and capability'
            }
            $script:sshServiceRemoved = $true
        }
    }

    if ($sshServiceRemoved -and $null -ne $sshConfigRoot) {
        Invoke-CleanupStep 'remove temporary sshd configuration' {
            if (Test-Path -LiteralPath $sshConfigRoot) {
                Remove-Item -LiteralPath $sshConfigRoot -Recurse -Force -ErrorAction Stop
            }
            if (Test-Path -LiteralPath $sshConfigRoot) {
                throw 'Temporary sshd configuration still exists'
            }
        }
    }

    if ($openSSHStateCaptured -and $openSSHInstallAttempted) {
        if (-not $sshServiceRemoved) {
            Register-CleanupFailure 'OpenSSH capability cannot be restored while the temporary sshd registration remains'
        } else {
            $openSSHCurrentState = $null
            Invoke-CleanupStep 'read OpenSSH capability during cleanup' {
                $script:openSSHCurrentState = [string](Get-WindowsCapability -Online -Name $capabilityName).State
            }
            if ($null -ne $openSSHCurrentState -and $openSSHCurrentState -ne $openSSHOriginalState) {
                if ($openSSHOriginalState -eq 'NotPresent') {
                    Invoke-CleanupStep 'remove temporary OpenSSH Server capability' {
                        $removed = Remove-WindowsCapability -Online -Name $capabilityName
                        if ($removed.RestartNeeded) {
                            throw 'OpenSSH Server capability removal requires a restart'
                        }
                    }
                } else {
                    Register-CleanupFailure "OpenSSH capability original state $openSSHOriginalState cannot be restored automatically"
                }
            }
            Invoke-CleanupStep 'verify OpenSSH capability restoration' {
                $restoredState = [string](Get-WindowsCapability -Online -Name $capabilityName).State
                if ($restoredState -ne $openSSHOriginalState) {
                    throw "OpenSSH capability state is $restoredState, expected $openSSHOriginalState"
                }
            }
        }
    }

    if ($userCreated -and $sshServiceRemoved) {
        Invoke-CleanupStep 'remove temporary fixture user' {
            Remove-LocalUser -Name $userName -ErrorAction Stop
            if ($null -ne (Get-LocalUser -Name $userName -ErrorAction SilentlyContinue)) {
                throw 'Temporary fixture user still exists'
            }
        }
    }

    if ($tokenFilterChanged) {
        Invoke-CleanupStep 'restore local account token filter policy' {
            if ($tokenFilterExisted) {
                $null = New-ItemProperty -Path $tokenFilterPath -Name 'LocalAccountTokenFilterPolicy' -PropertyType DWord -Value $tokenFilterValue -Force
            } else {
                Remove-ItemProperty -Path $tokenFilterPath -Name 'LocalAccountTokenFilterPolicy' -ErrorAction Stop
            }
        }
    }

    if ($winRMListenersCaptured) {
        Invoke-CleanupStep 'start WinRM for listener restoration' {
            Start-Service WinRM -ErrorAction Stop
        }
        $currentWinRMListeners = @()
        $winRMListenersEnumerated = $false
        Invoke-CleanupStep 'enumerate WinRM listeners for restoration' {
            $script:currentWinRMListeners = @(Get-WSManInstance -ResourceURI winrm/config/listener -Enumerate)
            $script:winRMListenersEnumerated = $true
        }
        if ($winRMListenersEnumerated) {
            foreach ($listener in $currentWinRMListeners) {
                Invoke-CleanupStep "remove temporary WinRM $($listener.Transport) listener $($listener.Address)" {
                    Remove-WSManInstance -ResourceURI winrm/config/listener -SelectorSet @{ Transport = $listener.Transport; Address = $listener.Address }
                }
            }
            foreach ($savedListener in $winRMListeners) {
                Invoke-CleanupStep "restore WinRM $($savedListener.Transport) listener $($savedListener.Address)" {
                    $selectorSet = @{
                        Transport = $savedListener.Transport
                        Address = $savedListener.Address
                    }
                    $valueSet = @{
                        Port = $savedListener.Port
                    }
                    if ($savedListener.Hostname -ne '') {
                        $valueSet.Hostname = $savedListener.Hostname
                    }
                    if ($null -ne $savedListener.Enabled -and ([string]$savedListener.Enabled) -ne '') {
                        $valueSet.Enabled = $savedListener.Enabled
                    }
                    if ($savedListener.URLPrefix -ne '') {
                        $valueSet.URLPrefix = $savedListener.URLPrefix
                    }
                    if ($savedListener.CertificateThumbprint -ne '') {
                        $valueSet.CertificateThumbprint = $savedListener.CertificateThumbprint
                    }
                    $null = New-WSManInstance -ResourceURI winrm/config/listener -SelectorSet $selectorSet -ValueSet $valueSet
                }
            }
            Invoke-CleanupStep 'verify WinRM listener restoration' {
                $restoredListeners = @(Get-WSManInstance -ResourceURI winrm/config/listener -Enumerate)
                if ($restoredListeners.Count -ne $winRMListeners.Count) {
                    throw "WinRM listener count is $($restoredListeners.Count), expected $($winRMListeners.Count)"
                }
                foreach ($savedListener in $winRMListeners) {
                    $matches = @($restoredListeners | Where-Object {
                        $_.Transport -eq $savedListener.Transport -and $_.Address -eq $savedListener.Address -and ([string]$_.Port) -eq $savedListener.Port
                    })
                    if ($matches.Count -ne 1) {
                        throw "WinRM listener was not restored: $($savedListener.Transport) $($savedListener.Address) $($savedListener.Port)"
                    }
                    $actual = $matches[0]
                    foreach ($property in @('Hostname', 'URLPrefix', 'CertificateThumbprint', 'Enabled')) {
                        if (([string]$actual.$property) -ne ([string]$savedListener.$property)) {
                            throw "WinRM listener property $property was not restored for $($savedListener.Transport) $($savedListener.Address)"
                        }
                    }
                }
            }
        }
    }

    if ($winRMServiceCaptured) {
        Invoke-CleanupStep 'restore WinRM startup type' {
            Set-Service -Name WinRM -StartupType $winRMStartType -ErrorAction Stop
        }
        Invoke-CleanupStep 'restore WinRM running state' {
            if ($winRMStatus -eq 'Running') {
                Start-Service WinRM -ErrorAction Stop
            } else {
                Stop-Service WinRM -ErrorAction Stop
            }
        }
        Invoke-CleanupStep 'verify WinRM service restoration' {
            $service = Get-Service -Name WinRM
            if ($service.StartType -ne $winRMStartType -or ([string]$service.Status) -ne $winRMStatus) {
                throw "WinRM service is $($service.StartType)/$($service.Status), expected $winRMStartType/$winRMStatus"
            }
        }
    }

    if ($null -ne $workRoot) {
        Invoke-CleanupStep 'remove second-volume fixture directory' {
            if (Test-Path -LiteralPath $workRoot) {
                Remove-Item -LiteralPath $workRoot -Recurse -Force -ErrorAction Stop
            }
            if (Test-Path -LiteralPath $workRoot) {
                throw 'Second-volume fixture directory still exists'
            }
        }
    }

    if ($null -ne $vhdPath) {
        $diskImageAttached = $false
        Invoke-CleanupStep 'inspect VHD attachment' {
            if (Test-Path -LiteralPath $vhdPath) {
                $diskImage = Get-DiskImage -ImagePath $vhdPath -ErrorAction Stop
                $script:diskImageAttached = [bool]$diskImage.Attached
            }
        }
        if ($diskImageAttached) {
            Invoke-CleanupStep 'detach VHD' {
                @(
                    'select vdisk file="' + $vhdPath + '"',
                    'detach vdisk'
                ) | Set-Content -Path $diskpartDetachScript -Encoding ASCII
                & diskpart.exe /s $diskpartDetachScript | Out-Host
                if ($LASTEXITCODE -ne 0) {
                    throw "DiskPart detach failed with exit code $LASTEXITCODE"
                }
            }
            Invoke-CleanupStep 'verify VHD detachment' {
                $diskImage = Get-DiskImage -ImagePath $vhdPath -ErrorAction Stop
                if ($diskImage.Attached) {
                    throw 'VHD is still attached'
                }
            }
        }
        foreach ($temporaryScript in @($diskpartCreateScript, $diskpartDetachScript)) {
            if ($null -ne $temporaryScript) {
                Invoke-CleanupStep "remove DiskPart temporary script $temporaryScript" {
                    if (Test-Path -LiteralPath $temporaryScript) {
                        Remove-Item -LiteralPath $temporaryScript -Force -ErrorAction Stop
                    }
                    if (Test-Path -LiteralPath $temporaryScript) {
                        throw "DiskPart temporary script still exists: $temporaryScript"
                    }
                }
            }
        }
        Invoke-CleanupStep 'remove VHD file' {
            if (Test-Path -LiteralPath $vhdPath) {
                Remove-Item -LiteralPath $vhdPath -Force -ErrorAction Stop
            }
            if (Test-Path -LiteralPath $vhdPath) {
                throw 'VHD file still exists'
            }
        }
    }
}

if ($null -ne $failure) {
    [Console]::Error.WriteLine('Real Windows tests failed: ' + $failure.ToString())
}
if ($cleanupFailures.Count -ne 0) {
    [Console]::Error.WriteLine("Fixture cleanup failures: $($cleanupFailures.Count)")
    foreach ($cleanupFailure in $cleanupFailures) {
        [Console]::Error.WriteLine(' - ' + $cleanupFailure)
    }
}
if ($null -ne $failure -or $cleanupFailures.Count -ne 0) {
    exit 1
}
Write-Host 'Real Windows Go, second-volume, WinRM and OpenSSH tests passed'
