<?php
/**
 * WPHPanel — phpMyAdmin SSO Signon Script
 * Clean implementation avoiding session name conflicts.
 */

$token = $_GET["token"] ?? "";
if (empty($token)) {
    die_access_denied();
}

$jwtSecret = "";
foreach (["/opt/wphpanel/.jwt_secret", "/opt/wphpanellite/.jwt_secret"] as $secretPath) {
    if (is_readable($secretPath)) {
        $jwtSecret = trim(file_get_contents($secretPath));
        break;
    }
}
if ($jwtSecret === "") { die_access_denied(); }

$parts = explode(".", $token, 2);
if (count($parts) !== 2) { die_access_denied(); }
list($payloadB64, $signature) = $parts;
$expectedSig = hash_hmac("sha256", $payloadB64, $jwtSecret);
if (!hash_equals($expectedSig, $signature)) { die_access_denied(); }

$payload = json_decode(base64_decode($payloadB64), true);
if (!$payload || !isset($payload["user"]) || !isset($payload["password"]) || !isset($payload["db"])) { die_access_denied(); }
if (time() > ($payload["exp"] ?? 0)) { die_access_denied(); }

$dbUser = $payload["user"];
$dbPass = $payload["password"];
$dbName = $payload["db"];

// CRITICAL: Expire ALL phpMyAdmin cookies by setting them to the past
setcookie("phpMyAdmin", "", 1, "/phpmyadmin/");
setcookie("phpMyAdmin_https", "", 1, "/phpmyadmin/");
setcookie("phpMyAdmin", "", 1, "/");
setcookie("phpMyAdmin_https", "", 1, "/");
setcookie("pma_lang", "", 1, "/phpmyadmin/");
setcookie("pma_lang_https", "", 1, "/phpmyadmin/");
setcookie("wphpanel_pma_signon", "", 1, "/");
setcookie("SignonSession", "", 1, "/");

// Delete server-side session files for old phpMyAdmin sessions
$sessDir = session_save_path() ?: "/var/lib/php/sessions";
if (isset($_COOKIE["phpMyAdmin_https"])) {
    $sessFile = $sessDir . "/sess_" . preg_replace("/[^a-zA-Z0-9,-]/", "", $_COOKIE["phpMyAdmin_https"]);
    @unlink($sessFile);
}
if (isset($_COOKIE["SignonSession"])) {
    $sessFile = $sessDir . "/sess_" . preg_replace("/[^a-zA-Z0-9,-]/", "", $_COOKIE["SignonSession"]);
    @unlink($sessFile);
}

// Create fresh SignonSession
session_set_cookie_params([
    "lifetime" => 0, "path" => "/", "secure" => true,
    "httponly" => true, "samesite" => "Lax"
]);
session_name("SignonSession");
session_start();
session_regenerate_id(true);

$_SESSION["PMA_single_signon_user"] = $dbUser;
$_SESSION["PMA_single_signon_password"] = $dbPass;
$_SESSION["PMA_single_signon_host"] = "localhost";
$_SESSION["PMA_single_signon_port"] = 3306;

session_write_close();

header("Location: /phpmyadmin/index.php?db=" . urlencode($dbName));
exit;

function die_access_denied() {
    http_response_code(403);
    echo "<h2>Access Denied</h2><p>Use the WPHPanel database manager to access phpMyAdmin.</p>";
    echo "<a href=\"/\">Back to Panel</a>";
    exit;
}
