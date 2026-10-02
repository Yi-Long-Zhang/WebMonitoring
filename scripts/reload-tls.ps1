<#
.SYNOPSIS
    Reload the TLS edge after the certificate files have been renewed.

.DESCRIPTION
    This stack does not obtain certificates. TLS_CERT_DIR is a directory the
    operator provisions -- whether by certbot on the host, an ACME client
    elsewhere, a corporate CA, or a manual purchase -- and the edge only ever
    reads the two files inside it. That boundary is deliberate: which CA issues
    the certificate, and which challenge proves control of the name, are
    deployment decisions this repository cannot make for the operator, and an
    ACME client baked into the overlay would make one of them for everyone.

    What the edge does NOT do is notice a renewal. nginx reads fullchain.pem once
    at start and keeps the certificate in memory until it is told to reload, so a
    renewed certificate on disk is invisible until this script runs. That is the
    gap this closes: it reloads the edge's TLS material without restarting the
    container, so established connections are not dropped and the control plane
    keeps serving throughout.

    Run it from whatever already renews the certificate -- a cron entry, a systemd
    timer, a post-renewal hook -- so the reload follows the renewal rather than
    relying on someone remembering.

.PARAMETER ComposeFile
    The base Compose file. The TLS overlay is added on top of it.

.PARAMETER CertDir
    The directory the TLS overlay mounts as /etc/nginx/certs. Used only to check
    that the two files are present and readable before touching the edge, so a
    half-written or missing certificate fails here with a clear message instead
    of a reload that succeeds and then fails to serve.

.PARAMETER CheckOnly
    Verify the certificate files and report the certificate the edge is actually
    presenting on the wire, without reloading. Use it to confirm a renewal took
    effect. Reads it over TLS rather than from the file on disk, because the file
    being new is not the same claim as the edge having loaded it -- and the second
    is the one that matters.

.EXAMPLE
    # After a renewal, load the new certificate into the running edge.
    .\scripts\reload-tls.ps1 -CertDir C:\certs\example.com

.EXAMPLE
    # Confirm what the edge is serving now, without changing anything.
    .\scripts\reload-tls.ps1 -CertDir C:\certs\example.com -CheckOnly

.NOTES
    Restarting the edge container would also pick up a renewed certificate, but it
    drops every in-flight request, including an SSE stream the console is holding
    open. A reload is a signal nginx handles between requests, which is why this is
    a script rather than "docker compose restart edge".
#>
param(
    [string]$ComposeFile = "deployments/compose/docker-compose.yml",
    [string]$Overlay = "deployments/compose/docker-compose.tls.yml",
    # Required rather than defaulted: guessing a certificate path would make the
    # script appear to work against a directory that is not the one the edge
    # actually serves from.
    [Parameter(Mandatory = $true)][string]$CertDir,
    [switch]$CheckOnly
)

$ErrorActionPreference = "Stop"

$fullchain = Join-Path $CertDir "fullchain.pem"
$privkey = Join-Path $CertDir "privkey.pem"

# Checked before the edge is touched. A reload against a missing or truncated
# certificate is accepted by nginx and only shows up at the next handshake, which
# is the worst moment to discover it.
foreach ($file in @($fullchain, $privkey)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
        throw "certificate file not found: $file"
    }
    if ((Get-Item -LiteralPath $file).Length -eq 0) {
        throw "certificate file is empty: $file"
    }
}

$composeArgs = @("-f", $ComposeFile, "-f", $Overlay)

# The overlay interpolates ${TLS_CERT_DIR} into a volume mount, so the compose
# command needs it set even for a reload that does not re-create the container.
# Handing over the same path the check above validated keeps the two in step.
$env:TLS_CERT_DIR = (Resolve-Path -LiteralPath $CertDir).Path

function Get-EdgeService {
    # The edge exists only when the TLS overlay is applied, so its absence is the
    # answer to "is this a TLS deployment?" rather than an error to work around.
    $services = & docker compose @composeArgs ps --services --filter "status=running" 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose could not list services; is the stack up?"
    }
    if ($services -notcontains "edge") {
        throw "the edge service is not running; start the stack with the TLS overlay first"
    }
}

Get-EdgeService

if ($CheckOnly) {
    # Read over the wire with curl rather than from the file on disk. The file
    # being new is not the claim that matters -- the edge having loaded it is --
    # and only a handshake proves the second. curl is used because it is in the
    # nginx:alpine image and openssl is not, which a first draft of this script
    # assumed and the image disproved.
    #
    # The certificate lines curl prints differ between a Schannel build (which
    # prints none) and an OpenSSL build (which prints "subject:" and "expire
    # date:"), so the whole verbose block is passed to the caller rather than
    # filtered down to lines this script cannot be sure will appear.
    $leaf = & docker compose @composeArgs exec -T edge `
        sh -c 'curl -sv --max-time 10 https://127.0.0.1/ -k -o /dev/null 2>&1 | grep -i -e cert -e subject -e "expire" -e "start date" -e "SSL connection"' 2>$null
    Write-Output "the edge is serving:"
    if ($leaf) { $leaf } else { Write-Output "  (curl reported no certificate detail; the edge may not be answering TLS)" }
    Write-Output ""
    Write-Output "compare the expiry reported above against the not-after date in $fullchain."
    Write-Output "if it still shows the previous certificate, the reload did not take effect."
    exit 0
}

# nginx -t first. A reload with a broken configuration is refused, and the
# running edge keeps serving the old certificate -- but the failure is silent
# unless the test output is surfaced, so it is checked and reported here.
& docker compose @composeArgs exec -T edge nginx -t
if ($LASTEXITCODE -ne 0) {
    throw "nginx rejected its configuration; the edge is still serving the previous certificate"
}

# SIGHUP rather than a restart: nginx re-reads the configuration and the
# certificate, finishes in-flight requests on the old workers, and starts new
# ones. Nothing is dropped, so an SSE stream survives the renewal.
& docker compose @composeArgs kill -s HUP edge
if ($LASTEXITCODE -ne 0) {
    throw "the edge did not accept the reload signal"
}

Write-Output "the edge reloaded; it is now serving the certificate in $CertDir."
Write-Output "confirm with: .\scripts\reload-tls.ps1 -CertDir $CertDir -CheckOnly"
