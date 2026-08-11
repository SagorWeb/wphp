package main

const installerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>WPHPanel — Server Installer</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--bg:#0b0f1a;--card:#111827;--border:#1e293b;--accent:#6366f1;--accent2:#818cf8;--text:#e2e8f0;--dim:#64748b;--green:#22c55e;--red:#ef4444}
body{min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--bg);font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:var(--text)}
.wrap{width:100%;max-width:580px;padding:24px}
.logo{text-align:center;margin-bottom:32px}
.logo h1{font-size:24px;font-weight:800;background:linear-gradient(135deg,var(--accent),var(--accent2));-webkit-background-clip:text;-webkit-text-fill-color:transparent;letter-spacing:-.5px}
.logo p{color:var(--dim);font-size:13px;margin-top:6px}
.card{background:var(--card);border:1px solid var(--border);border-radius:16px;padding:32px;margin-bottom:20px}
.sysinfo{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:24px}
.si{background:rgba(99,102,241,.06);border:1px solid rgba(99,102,241,.12);border-radius:10px;padding:12px 14px}
.si label{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:1px;color:var(--dim);display:block;margin-bottom:4px}
.si span{font-size:15px;font-weight:600;color:var(--text)}
.field{margin-bottom:18px}
.field label{display:block;font-size:12px;font-weight:600;color:var(--dim);margin-bottom:6px;text-transform:uppercase;letter-spacing:.5px}
.field input{width:100%;padding:12px 14px;background:var(--bg);border:1px solid var(--border);border-radius:10px;color:var(--text);font-size:14px;outline:none;transition:border .2s}
.field input:focus{border-color:var(--accent)}
.field input::placeholder{color:#374151}
.btn{width:100%;padding:14px;border:none;border-radius:12px;font-size:15px;font-weight:700;cursor:pointer;transition:all .2s}
.btn-primary{background:linear-gradient(135deg,var(--accent),#4f46e5);color:#fff}
.btn-primary:hover{transform:translateY(-1px);box-shadow:0 8px 25px rgba(99,102,241,.3)}
.btn-primary:disabled{opacity:.5;cursor:not-allowed;transform:none;box-shadow:none}
.btn-danger{background:rgba(239,68,68,.1);color:var(--red);border:1px solid rgba(239,68,68,.2);margin-top:12px}
.btn-danger:hover{background:rgba(239,68,68,.2)}
.progress-wrap{margin-top:20px}
.step{display:flex;align-items:center;gap:12px;padding:10px 14px;border-radius:10px;margin-bottom:6px;transition:background .3s}
.step.active{background:rgba(99,102,241,.08)}
.step.done{background:rgba(34,197,94,.06)}
.step.error{background:rgba(239,68,68,.06)}
.step-num{width:28px;height:28px;border-radius:50%;display:flex;align-items:center;justify-content:center;font-size:11px;font-weight:700;flex-shrink:0;border:2px solid var(--border);color:var(--dim);transition:all .3s}
.step.active .step-num{border-color:var(--accent);color:var(--accent);animation:pulse 1.5s infinite}
.step.done .step-num{border-color:var(--green);background:var(--green);color:#fff}
.step.error .step-num{border-color:var(--red);background:var(--red);color:#fff}
.step-text{font-size:13px;font-weight:500;color:var(--dim)}
.step.active .step-text{color:var(--text)}
.step.done .step-text{color:var(--green)}
.bar-wrap{margin-top:16px;background:var(--bg);border-radius:8px;height:6px;overflow:hidden}
.bar{height:100%;background:linear-gradient(90deg,var(--accent),var(--green));border-radius:8px;transition:width .5s ease;width:0%}
.pct{text-align:center;font-size:12px;font-weight:600;color:var(--dim);margin-top:8px}
.creds{margin-top:20px}
.cred{display:flex;justify-content:space-between;padding:8px 12px;border-bottom:1px solid var(--border);font-size:12px}
.cred:last-child{border:none}
.cred .k{color:var(--dim);font-weight:600}
.cred .v{color:var(--accent2);font-family:monospace;font-weight:600;word-break:break-all;max-width:60%;text-align:right}
.success{text-align:center;padding:20px 0}
.success h2{font-size:22px;font-weight:800;color:var(--green);margin-bottom:8px}
.success p{color:var(--dim);font-size:13px;line-height:1.6}
.hidden{display:none}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.5}}
@keyframes spin{to{transform:rotate(360deg)}}
.spinner{width:14px;height:14px;border:2px solid transparent;border-top-color:var(--accent);border-radius:50%;animation:spin .6s linear infinite;display:inline-block;vertical-align:middle;margin-right:6px}
</style>
</head>
<body>
<div class="wrap">
<div class="logo">
<h1>WPHPanel</h1>
<p>Modern managed hosting for your server</p>
</div>

<!-- Step 1: Setup Form -->
<div id="formView" class="card">
<div class="sysinfo" id="sysinfo"><div class="si" style="grid-column:1/-1"><label>Loading</label><span>Detecting server...</span></div></div>
<div class="field"><label>Panel Hostname</label><input id="hostname" type="text" placeholder="panel.example.com"></div>
<div class="field"><label>Admin Email</label><input id="email" type="email" placeholder="admin@example.com"></div>
<div class="field"><label>Admin Password</label><input id="password" type="password" placeholder="Strong password (min 8 chars)"></div>
<button class="btn btn-primary" id="startBtn" onclick="startInstall()">Start Installation</button>
</div>

<!-- Step 2: Progress -->
<div id="progressView" class="card hidden">
<div id="steps"></div>
<div class="bar-wrap"><div class="bar" id="bar"></div></div>
<div class="pct" id="pctText">0%</div>
</div>

<!-- Step 3: Complete -->
<div id="completeView" class="card hidden">
<div class="success">
<h2>&#10003; Installation Complete</h2>
<p>Your server is provisioned! Upload backend &amp; frontend to finish setup.</p>
</div>
<div class="creds" id="creds"></div>
<div style="margin-top:16px;padding:14px;background:rgba(99,102,241,.06);border-radius:10px;font-size:12px;color:var(--dim);line-height:1.6">
<strong style="color:var(--text)">Next Steps:</strong><br>
1. Upload backend → <code style="color:var(--accent2)">/opt/wphpanel/bin/wphpanel-api</code><br>
2. Upload frontend → <code style="color:var(--accent2)">/var/www/wphpanel/</code><br>
3. Run: <code style="color:var(--accent2)">systemctl start wphpanel-api</code>
</div>
<button class="btn btn-danger" id="deleteBtn" onclick="deleteInstaller()">Delete Installer from Server</button>
</div>
</div>

<script>
const STEPS = [
  'System Update', 'Set Hostname', 'Official Repositories',
  'Nginx 1.30 (Stable)', 'MariaDB 11.4 + PHP 8.4 + phpMyAdmin', 'PostgreSQL 17 & pgAdmin 4',
  'SSL Manager (Lego)', 'ZFS + Incus 7.x (LTS) Engine', 'Valkey 9 & Finalize'
];

let pollInterval = null;

// Load server info
fetch('/api/sysinfo').then(r=>r.json()).then(d=>{
  document.getElementById('sysinfo').innerHTML =
    si('IP Address',d.ip)+si('OS',d.os)+si('CPU',d.cpu+' Core(s)')+
    si('RAM',d.ram_mb+' MB')+si('Disk',d.disk_gb+' GB')+si('Hostname',d.hostname);
  if(d.hostname && d.hostname!=='localhost') document.getElementById('hostname').value=d.hostname;
}).catch(()=>{});

// Check status on load for resilient recovery
checkInitialStatus();

function si(l,v){return '<div class="si"><label>'+l+'</label><span>'+(v||'—')+'</span></div>';}

function checkInitialStatus() {
  fetch('/api/status').then(r=>r.json()).then(d=>{
    if(d.status === 'running') {
      showProgressView();
      updateStep(d);
      startPolling();
    } else if(d.status === 'complete') {
      showComplete(d);
    }
  }).catch(()=>{});
}

function showProgressView() {
  document.getElementById('formView').classList.add('hidden');
  document.getElementById('progressView').classList.remove('hidden');
  
  // Render step list
  let html='';
  STEPS.forEach((s,i)=>{html+='<div class="step" id="s'+i+'"><div class="step-num">'+(i+1)+'</div><div class="step-text">'+s+'</div></div>';});
  document.getElementById('steps').innerHTML=html;
}

function startInstall(){
  const h=document.getElementById('hostname').value.trim();
  const e=document.getElementById('email').value.trim();
  const p=document.getElementById('password').value;
  if(!h){alert('Hostname is required');return;}
  if(!e||!e.includes('@')){alert('Valid email required');return;}
  if(!p||p.length<8){alert('Password must be at least 8 characters');return;}

  document.getElementById('startBtn').disabled=true;
  
  fetch('/api/install',{
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({hostname:h,admin_email:e,admin_password:p})
  }).then(r=>{
    if (r.status === 409) {
      alert('Installation already in progress!');
      window.location.reload();
      return;
    }
    return r.json();
  }).then(d=>{
    if(d && d.started) {
      showProgressView();
      startPolling();
    }
  }).catch(()=>{
    document.getElementById('startBtn').disabled=false;
  });
}

function startPolling() {
  if (pollInterval) clearInterval(pollInterval);
  pollInterval = setInterval(()=>{
    fetch('/api/status').then(r=>r.json()).then(d=>{
      if(d.status === 'running') {
        updateStep(d);
      } else if(d.status === 'complete') {
        clearInterval(pollInterval);
        showComplete(d);
      } else if(d.status === 'error') {
        clearInterval(pollInterval);
        alert('Installation failed: ' + (d.log || 'Unknown error'));
      }
    }).catch(()=>{});
  }, 1500);
}

function updateStep(d){
  STEPS.forEach((_,i)=>{
    const el=document.getElementById('s'+i);
    if(!el)return;
    const idx=i+1;
    if(idx<d.step){el.className='step done';el.querySelector('.step-num').textContent='✓';}
    else if(idx===d.step){
      el.className='step '+(d.status==='done'?'done':'active');
      if(d.status==='done')el.querySelector('.step-num').textContent='✓';
      else el.querySelector('.step-num').innerHTML='<span class="spinner"></span>';
    }
    else{el.className='step';}
  });
  document.getElementById('bar').style.width=d.percent+'%';
  document.getElementById('pctText').textContent=d.percent+'%';
}

function showComplete(d){
  document.getElementById('formView').classList.add('hidden');
  document.getElementById('progressView').classList.add('hidden');
  document.getElementById('completeView').classList.remove('hidden');
  const c=d.credentials||{};
  let html='';
  html+=cr('Panel Hostname',d.hostname);
  html+=cr('Admin Email',d.admin_email);
  html+=cr('MariaDB Root Pass',c.mariadb_root_pass);
  html+=cr('PostgreSQL Pass',c.postgres_pass);
  html+=cr('Valkey Pass',c.valkey_pass);
  html+=cr('JWT Secret',c.jwt_secret);
  html+=cr('API Binary','/opt/wphpanel/bin/wphpanel-api');
  html+=cr('Frontend','/var/www/wphpanel/');
  html+=cr('.env Config','/opt/wphpanel/.env');
  document.getElementById('creds').innerHTML=html;

  // Auto delete installer from server for security
  setTimeout(() => {
    fetch('/api/delete', { method: 'POST' }).then(() => {
      const btn = document.getElementById('deleteBtn');
      if (btn) {
        btn.textContent = 'Installer Automatically Deleted';
        btn.disabled = true;
        btn.style.opacity = '0.5';
        btn.style.cursor = 'default';
      }
    }).catch(() => {});
  }, 2000);
}

function cr(k,v){return '<div class="cred"><span class="k">'+k+'</span><span class="v">'+(v||'—')+'</span></div>';}

function deleteInstaller(){
  if(!confirm('Delete installer binary from this server?'))return;
  fetch('/api/delete',{method:'POST'}).then(()=>{
    alert('Installer deleted. This page will stop working.');
  }).catch(()=>{alert('Done — installer process stopped.');});
}
</script>
</body>
</html>`
