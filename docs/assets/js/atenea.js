(() => {
  const root = document.documentElement;
  const themeButton = document.querySelector('[data-theme-toggle]');
  const themeLabel = document.querySelector('[data-theme-label]');
  const themes = ['auto', 'light', 'dark'];
  const storedTheme = localStorage.getItem('atenea-theme') || 'auto';
  root.dataset.theme = storedTheme;

  const updateThemeLabel = () => {
    if (themeLabel) themeLabel.textContent = root.dataset.theme[0].toUpperCase() + root.dataset.theme.slice(1);
    if (themeButton) themeButton.setAttribute('aria-label', `Theme: ${root.dataset.theme}`);
  };
  updateThemeLabel();
  themeButton?.addEventListener('click', () => {
    const next = themes[(themes.indexOf(root.dataset.theme) + 1) % themes.length];
    root.dataset.theme = next;
    localStorage.setItem('atenea-theme', next);
    updateThemeLabel();
  });

  const menuButton = document.querySelector('[data-menu-toggle]');
  const menuCloseButtons = document.querySelectorAll('[data-menu-close]');
  const sidebar = document.querySelector('.sidebar');
  const mobileQuery = window.matchMedia('(max-width: 720px)');
  const syncMenuAccessibility = () => {
    if (root.dataset.menuOpen !== 'true') sidebar?.setAttribute('aria-hidden', String(mobileQuery.matches));
  };
  syncMenuAccessibility();
  mobileQuery.addEventListener?.('change', syncMenuAccessibility);
  const setMenuOpen = (open) => {
    root.dataset.menuOpen = open ? 'true' : 'false';
    menuButton?.setAttribute('aria-expanded', String(open));
    menuButton?.setAttribute('aria-label', open ? 'Close navigation' : 'Open navigation');
    sidebar?.setAttribute('aria-hidden', String(!open));
    if (open) sidebar?.querySelector('a')?.focus();
  };
  menuButton?.addEventListener('click', () => setMenuOpen(root.dataset.menuOpen !== 'true'));
  menuCloseButtons.forEach(button => button.addEventListener('click', () => setMenuOpen(false)));
  sidebar?.querySelectorAll('a').forEach(link => link.addEventListener('click', () => setMenuOpen(false)));

  const searchDialog = document.createElement('dialog');
  searchDialog.className = 'search-dialog';
  searchDialog.innerHTML = '<form method="dialog" class="search-panel"><div class="search-top"><input type="search" placeholder="Search Atenea docs" aria-label="Search documentation" autofocus><button value="cancel" aria-label="Close search">×</button></div><div class="search-results" aria-live="polite"></div></form>';
  document.body.append(searchDialog);
  const searchInput = searchDialog.querySelector('input');
  const searchResults = searchDialog.querySelector('.search-results');
  let index = [];
  try {
    index = JSON.parse(document.querySelector('#atenea-search-data')?.textContent || '[]').map(item => typeof item === 'string' ? JSON.parse(item) : item);
  } catch (_) {}
  const renderResults = (query = '') => {
    const normalized = query.trim().toLowerCase();
    const matches = normalized ? index.filter(item => `${item.title} ${item.section} ${item.text}`.toLowerCase().includes(normalized)).slice(0, 12) : index.slice(0, 8);
    searchResults.innerHTML = matches.length ? matches.map(item => `<a href="${item.url}"><strong>${escapeHtml(item.title)}</strong><small>${escapeHtml(item.section || 'Atenea')} · ${escapeHtml(item.text.replace(/\s+/g, ' ').trim().slice(0, 120))}</small></a>`).join('') : '<p class="search-empty">No matching documentation.</p>';
  };
  const escapeHtml = value => value.replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#039;'}[char]));
  const openSearch = () => { searchDialog.showModal(); searchInput.value = ''; renderResults(); searchInput.focus(); };
  document.querySelector('[data-search-open]')?.addEventListener('click', openSearch);
  searchInput.addEventListener('input', () => renderResults(searchInput.value));
  document.addEventListener('keydown', event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); openSearch(); }
    if (event.key === '/' && !['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName)) { event.preventDefault(); openSearch(); }
    if (event.key === 'Escape' && root.dataset.menuOpen === 'true') setMenuOpen(false);
  });
  searchDialog.addEventListener('click', event => { if (event.target === searchDialog) searchDialog.close(); });
})();
