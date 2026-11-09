// Apply the saved theme before styles load, avoiding a bright flash on navigation.
try {
 const saved=localStorage.getItem('paymenthub-theme');
 document.documentElement.dataset.bsTheme=saved==='light'?'light':'dark';
} catch { document.documentElement.dataset.bsTheme='dark'; }
