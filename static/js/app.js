// ─── Utilities ───────────────────────────────────────────────────────────
function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str == null ? '' : String(str);
    return div.innerHTML;
}

const ENGINE_TYPES = {
    mergetree:   { name: 'MergeTree' },
    replicated:  { name: 'Replicated' },
    distributed: { name: 'Distributed' },
    mview:       { name: 'MaterializedView' },
    dictionary:  { name: 'Dictionary' },
};

function classifyEngine(engineName) {
    if (!engineName) return 'mergetree';
    const e = engineName;
    if (e === 'Distributed') return 'distributed';
    if (e === 'MaterializedView') return 'mview';
    if (e.startsWith('Dictionary')) return 'dictionary';
    if (e.startsWith('Replicated')) return 'replicated';
    return 'mergetree';
}

function formatRows(rows) {
    if (rows == null) return '—';
    if (rows >= 1e9) return (rows / 1e9).toFixed(2) + 'B';
    if (rows >= 1e6) return (rows / 1e6).toFixed(1) + 'M';
    if (rows >= 1e3) return (rows / 1e3).toFixed(1) + 'K';
    return Number(rows).toLocaleString();
}

function formatBytes(bytes) {
    if (!bytes) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let size = bytes;
    let i = 0;
    while (size >= 1024 && i < units.length - 1) { size /= 1024; i++; }
    return `${size.toFixed(1)} ${units[i]}`;
}

// ─── DOM references ──────────────────────────────────────────────────────
const databaseTree         = document.getElementById('database-tree');
const refreshBtn           = document.getElementById('refresh-btn');
const currentSelection     = document.getElementById('current-selection');
const dataflowDiagram      = document.getElementById('dataflow-diagram');
const relationshipsDiagram = document.getElementById('relationships-diagram');
const exportHtmlBtn        = document.getElementById('export-html-btn');
const dbCountEl            = document.getElementById('db-count');

const dataflowZoomInBtn       = document.getElementById('dataflow-zoom-in-btn');
const dataflowZoomOutBtn      = document.getElementById('dataflow-zoom-out-btn');
const dataflowResetZoomBtn    = document.getElementById('dataflow-reset-zoom-btn');
const relationshipsZoomInBtn  = document.getElementById('relationships-zoom-in-btn');
const relationshipsZoomOutBtn = document.getElementById('relationships-zoom-out-btn');
const relationshipsResetZoomBtn = document.getElementById('relationships-reset-zoom-btn');

const sectionTabs          = document.querySelectorAll('.section-tab');
const dataFlowSection      = document.getElementById('data-flow-section');
const relationshipsSection = document.getElementById('relationships-section');

const tableDetailsContainer = document.querySelector('.table-details-container');
const tableDetailsContent   = document.getElementById('table-details');

const connNameEl         = document.getElementById('conn-name');
const connPortEl         = document.getElementById('conn-port');
const sidebarFilterInput = document.getElementById('sidebar-filter-input');
const paletteBtn         = document.getElementById('open-palette-btn');
const paletteShortcutKbd = document.getElementById('palette-shortcut-kbd');
const paletteEl          = document.getElementById('cmd-palette');
const paletteInput       = document.getElementById('palette-input');
const paletteResultsEl   = document.getElementById('palette-results');

// ─── State ───────────────────────────────────────────────────────────────
let databases = [];
let selectedDatabase = null;
let selectedTable = null;
let currentDataFlowGraph = null;
let currentRelationshipsGraph = null;
let currentActiveSection = 'data-flow';

let dataflowPanzoom = null;
let relationshipsPanzoom = null;

// ─── Boot ────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
    loadConnectionInfo();
    loadDatabases();

    refreshBtn.addEventListener('click', loadDatabases);
    exportHtmlBtn.addEventListener('click', exportHtml);

    dataflowZoomInBtn.addEventListener('click',    () => dataflowPanzoom && dataflowPanzoom.zoomIn());
    dataflowZoomOutBtn.addEventListener('click',   () => dataflowPanzoom && dataflowPanzoom.zoomOut());
    dataflowResetZoomBtn.addEventListener('click', () => dataflowPanzoom && dataflowPanzoom.reset());

    relationshipsZoomInBtn.addEventListener('click',    () => relationshipsPanzoom && relationshipsPanzoom.zoomIn());
    relationshipsZoomOutBtn.addEventListener('click',   () => relationshipsPanzoom && relationshipsPanzoom.zoomOut());
    relationshipsZoomResetIfAny();

    sectionTabs.forEach((tab) => {
        tab.addEventListener('click', () => switchSection(tab.dataset.section));
    });

    const savedActiveSection = localStorage.getItem('activeSection');
    if (savedActiveSection === 'relationships') switchSection('relationships');

    const databaseHeader = document.getElementById('database-header');
    const sidebar = document.querySelector('.sidebar');
    if (databaseHeader && sidebar) {
        databaseHeader.addEventListener('click', () => {
            databaseHeader.classList.toggle('collapsed');
            sidebar.classList.toggle('database-collapsed');
            localStorage.setItem('databaseHeaderCollapsed', databaseHeader.classList.contains('collapsed'));
        });
        if (localStorage.getItem('databaseHeaderCollapsed') === 'true') {
            databaseHeader.classList.add('collapsed');
            sidebar.classList.add('database-collapsed');
        }
    }

    const tableTypesHeader = document.querySelector('.legend-container .collapsible-header');
    if (tableTypesHeader) {
        tableTypesHeader.addEventListener('click', () => {
            tableTypesHeader.classList.toggle('collapsed');
            localStorage.setItem('tableTypesCollapsed', tableTypesHeader.classList.contains('collapsed'));
        });
        if (localStorage.getItem('tableTypesCollapsed') === 'true') {
            tableTypesHeader.classList.add('collapsed');
        }
    }

    const metadataToggle = document.getElementById('metadata-toggle');
    if (metadataToggle) {
        metadataToggle.addEventListener('change', toggleMetadataVisibility);
        const isVisible = localStorage.getItem('metadataVisible') === 'true';
        metadataToggle.checked = isVisible;
        updateMetadataVisibility(isVisible);
    }

    const tableDetailsHeader = document.querySelector('.table-details-header');
    if (tableDetailsHeader) {
        tableDetailsHeader.addEventListener('click', toggleTableDetails);
        const isVisible = localStorage.getItem('tableDetailsVisible') !== 'false';
        if (!isVisible) tableDetailsContainer.classList.add('collapsed');
    }

    setupSidebarFilter();
    setupCommandPalette();
});

function relationshipsZoomResetIfAny() {
    relationshipsResetZoomBtn.addEventListener('click', () => relationshipsPanzoom && relationshipsPanzoom.reset());
}

// ─── Data loading ────────────────────────────────────────────────────────
async function loadConnectionInfo() {
    try {
        const response = await fetch('/api/connection');
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const info = await response.json();
        if (connNameEl) connNameEl.textContent = info.host || '';
        if (connPortEl) connPortEl.textContent = info.port != null ? String(info.port) : '';
    } catch (error) {
        console.error('Error loading connection info:', error);
    }
}

async function loadDatabases() {
    try {
        const response = await fetch('/api/databases');
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        databases = await response.json();
        renderDatabaseTree();
    } catch (error) {
        console.error('Error loading databases:', error);
        showError('Failed to load databases. Check the ClickHouse connection.');
    }
}

function renderDatabaseTree() {
    databaseTree.innerHTML = '';

    let dbList = [];
    if (typeof databases === 'object' && !Array.isArray(databases)) {
        dbList = Object.entries(databases).map(([name, content]) => ({
            name,
            tables: content && typeof content === 'object' ? content : {},
        }));
    } else if (Array.isArray(databases)) {
        dbList = databases.map((db) => ({
            name: db.name || String(db),
            tables: db.tables || {},
        }));
    }

    if (dbCountEl) dbCountEl.textContent = dbList.length;

    dbList.forEach(({ name, tables }) => {
        const dbItem = document.createElement('li');

        const dbSpan = document.createElement('span');
        dbSpan.className = 'database';

        const tableEntries = Array.isArray(tables)
            ? tables.map((t) => [typeof t === 'string' ? t : t.name, ''])
            : Object.entries(tables);

        dbSpan.dataset.count = tableEntries.length;

        const nameEl = document.createElement('span');
        nameEl.className = 'db-name';
        nameEl.textContent = name;
        dbSpan.appendChild(nameEl);

        dbSpan.addEventListener('click', () => toggleDatabase(dbItem, dbSpan));

        dbItem.appendChild(dbSpan);

        const tablesList = document.createElement('ul');
        tablesList.style.display = 'none';

        tableEntries.forEach(([tableName, displayHtml]) => {
            addTableToList(tablesList, name, tableName, displayHtml);
        });

        dbItem.appendChild(tablesList);
        databaseTree.appendChild(dbItem);
    });
}

function addTableToList(tablesList, dbName, dbTable, showHtml) {
    const tableItem = document.createElement('li');
    tableItem.className = 'table';

    const iconMatch = typeof showHtml === 'string' ? showHtml.match(/<i [^>]*><\/i>/) : null;
    const iconHtml = iconMatch ? iconMatch[0] : '';
    const rest = (typeof showHtml === 'string' ? showHtml.replace(/<i [^>]*><\/i>/, '') : dbTable).trim();

    tableItem.innerHTML = `${iconHtml}<span class="table-text">${rest || escapeHtml(dbTable)}</span>`;
    tableItem.dataset.database = dbName;
    tableItem.dataset.table = dbTable;
    tableItem.title = dbTable;

    tableItem.addEventListener('click', () => selectTable(tableItem));

    tablesList.appendChild(tableItem);
}

function toggleDatabase(dbItem, dbSpan) {
    const tablesList = dbItem.querySelector('ul');
    const isOpen = tablesList.style.display !== 'none';
    tablesList.style.display = isOpen ? 'none' : 'block';
    dbSpan.classList.toggle('open', !isOpen);
}

async function selectTable(tableItem) {
    const previouslySelected = document.querySelector('.tree-view .table.selected');
    if (previouslySelected) previouslySelected.classList.remove('selected');
    tableItem.classList.add('selected');

    selectedDatabase = tableItem.dataset.database;
    selectedTable = tableItem.dataset.table;

    renderBreadcrumb({ database: selectedDatabase, table: selectedTable, engine: null });

    await Promise.all([loadTableGraphs(), loadTableDetails(selectedDatabase, selectedTable)]);
}

function selectTableByID(id) {
    if (!id) return;
    const item = document.querySelector(`.tree-view .table[data-database="${cssEscape(id.split('.')[0])}"][data-table="${cssEscape(id.split('.').slice(1).join('.'))}"]`);
    if (item) {
        // ensure the parent db is expanded
        const dbLi = item.closest('li').parentElement.closest('li');
        if (dbLi) {
            const dbSpan = dbLi.querySelector(':scope > .database');
            const ul = dbLi.querySelector(':scope > ul');
            if (dbSpan && ul && ul.style.display === 'none') toggleDatabase(dbLi, dbSpan);
        }
        item.scrollIntoView({ block: 'nearest' });
        selectTable(item);
    }
}

function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(s);
    return String(s).replace(/["'\\]/g, '\\$&');
}

function renderBreadcrumb({ database, table, engine }) {
    const engineKey = engine ? classifyEngine(engine) : null;
    const chipHtml = engineKey
        ? `<span class="crumb-chip ${engineKey}">${escapeHtml(ENGINE_TYPES[engineKey].name)}</span>`
        : '';
    const engineDetail = engine && engineKey && engine !== ENGINE_TYPES[engineKey].name
        ? `<span class="crumb-engine"> · ${escapeHtml(engine)}</span>`
        : '';

    currentSelection.innerHTML = `
        <span class="crumb-root">schema</span>
        <span class="crumb-sep">/</span>
        <span class="crumb-db">${escapeHtml(database)}</span>
        <span class="crumb-sep">/</span>
        <span class="crumb-tb">${escapeHtml(table)}</span>
        ${chipHtml}${engineDetail}
    `;
}

// ─── Section switching ───────────────────────────────────────────────────
function switchSection(sectionName) {
    sectionTabs.forEach((tab) => {
        tab.classList.toggle('active', tab.dataset.section === sectionName);
    });
    if (sectionName === 'data-flow') {
        dataFlowSection.classList.remove('hidden');
        relationshipsSection.classList.add('hidden');
        currentActiveSection = 'data-flow';
    } else {
        dataFlowSection.classList.add('hidden');
        relationshipsSection.classList.remove('hidden');
        currentActiveSection = 'relationships';
    }
    localStorage.setItem('activeSection', sectionName);
}

// ─── Graph loading + rendering ───────────────────────────────────────────
async function loadTableGraphs() {
    if (!selectedDatabase || !selectedTable) return;
    try {
        const [flowResp, relResp] = await Promise.all([
            fetch(`/api/dataflow/${selectedDatabase}/${selectedTable}`),
            fetch(`/api/relationships/${selectedDatabase}/${selectedTable}`),
        ]);
        if (!flowResp.ok) throw new Error(`Data Flow: HTTP ${flowResp.status}`);

        currentDataFlowGraph = await flowResp.json();
        currentRelationshipsGraph = relResp.ok ? await relResp.json() : { tables: [], edges: [] };

        renderDataFlow();
        renderRelationships();
    } catch (error) {
        console.error('Error loading graphs:', error);
        showError('Failed to load table graphs.');
    }
}

function renderDataFlow() {
    if (!window.SchemaDiagram) return;
    const result = window.SchemaDiagram.renderDataFlow(dataflowDiagram, currentDataFlowGraph, {
        onNodeClick: (id) => selectTableByID(id),
    });
    dataflowPanzoom = result ? result.panzoom : null;
}

function renderRelationships() {
    if (!window.SchemaDiagram) return;
    const result = window.SchemaDiagram.renderRelationships(relationshipsDiagram, currentRelationshipsGraph, {
        onTableClick: (id) => selectTableByID(id),
    });
    relationshipsPanzoom = result ? result.panzoom : null;
}

// ─── Toggles ─────────────────────────────────────────────────────────────
function toggleMetadataVisibility() {
    const metadataToggle = document.getElementById('metadata-toggle');
    const isVisible = metadataToggle.checked;
    updateMetadataVisibility(isVisible);
    localStorage.setItem('metadataVisible', isVisible);
}

function updateMetadataVisibility(isVisible) {
    const sidebar = document.querySelector('.sidebar');
    if (sidebar) sidebar.classList.toggle('metadata-visible', isVisible);
}

function toggleTableDetails() {
    tableDetailsContainer.classList.toggle('collapsed');
    const isVisible = !tableDetailsContainer.classList.contains('collapsed');
    localStorage.setItem('tableDetailsVisible', isVisible);
}

// ─── Table details panel ─────────────────────────────────────────────────
async function loadTableDetails(database, table) {
    if (!database || !table) return;
    try {
        const response = await fetch(`/api/table/${database}/${table}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const details = await response.json();
        renderTableDetails(details);
        renderBreadcrumb({ database: details.database, table: details.name, engine: details.engine });
    } catch (error) {
        console.error('Error loading table details:', error);
        showTableDetailsError('Failed to load table details.');
    }
}

function renderTableDetails(details) {
    if (!details) {
        showTableDetailsError('No table details available.');
        return;
    }

    const engineKey = classifyEngine(details.engine);
    const cols = Array.isArray(details.columns) ? details.columns : [];

    const html = `
        <div class="table-info">
            <h4>information</h4>
            <div class="table-info-grid">
                <span class="table-info-label">name</span>
                <span class="table-info-value" style="font-weight:600">${escapeHtml(details.name)}</span>
                <span class="table-info-label">database</span>
                <span class="table-info-value">${escapeHtml(details.database)}</span>
                <span class="table-info-label">engine</span>
                <span class="table-info-value engine-chip ${engineKey}">${escapeHtml(details.engine)}</span>
                <span class="table-info-label">rows</span>
                <span class="table-info-value numeric">${details.total_rows != null ? formatRows(details.total_rows) : '—'}</span>
                <span class="table-info-label">size</span>
                <span class="table-info-value">${formatBytes(details.total_bytes)}</span>
            </div>
        </div>

        <div class="columns-section">
            <h4>columns <span class="col-count">${cols.length} total</span></h4>
            <table class="columns-table">
                <thead>
                    <tr><th>Name</th><th>Type</th></tr>
                </thead>
                <tbody>
                    ${cols.map((column) => `
                        <tr>
                            <td class="col-content">
                                <span class="column-name" title="${escapeHtml(column.name)}">${escapeHtml(column.name)}</span>
                                <span class="column-type" title="${escapeHtml(column.type)}">${escapeHtml(column.type)}</span>
                            </td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        </div>
    `;
    tableDetailsContent.innerHTML = html;
}

function showTableDetailsError(message) {
    tableDetailsContent.innerHTML = `
        <div class="no-table-selected">
            <p>${escapeHtml(message)}</p>
        </div>
    `;
}

function showError(message) {
    console.error(message);
    alert(message);
}

// ─── Export HTML ─────────────────────────────────────────────────────────
// Embed the rendered SVG directly so the exported file has zero external
// dependencies (no CDN, no Mermaid, no Dagre).
function exportHtml() {
    if (!selectedDatabase || !selectedTable) {
        showError('No table selected.');
        return;
    }
    const sourceDiagram = currentActiveSection === 'data-flow' ? dataflowDiagram : relationshipsDiagram;
    const svg = sourceDiagram.querySelector('svg');
    if (!svg) {
        showError('Nothing to export yet — wait for the diagram to render.');
        return;
    }

    // Inline a clone with computed styles preserved via a <style> block.
    const clone = svg.cloneNode(true);
    // The viewport's transform was used for pan/zoom. Reset for the export.
    const viewport = clone.querySelector('.viewport');
    if (viewport) viewport.removeAttribute('transform');

    const wrapStyles = `
:root { --accent:#3b5bdb; --border:#e8e8ea; --bg:#fafafa; --text:#0b0d12; --muted:#6b7280; --faint:#9aa0aa;
  --t-mergetree-fg:#1f6feb; --t-replicated-fg:#d97706; --t-distributed-fg:#0d9488; --t-mview-fg:#a855f7; --t-dictionary-fg:#b48a00; }
*, *::before, *::after { box-sizing: border-box; }
body { margin:0; font-family:'JetBrains Mono', ui-monospace, monospace; background:var(--bg); color:var(--text); }
header { padding:14px 20px; border-bottom:1px solid var(--border); background:#fff; }
header h1 { margin:0; font-size:13px; font-weight:600; }
header .crumb { font-size:11px; color:var(--muted); margin-top:4px; }
.canvas { padding:24px; min-height:calc(100vh - 56px);
  background-image: radial-gradient(circle, #d8dadf 1px, transparent 1px); background-size: 20px 20px; }
svg { width:100%; height:auto; max-height:calc(100vh - 110px); }
${commonDiagramCss()}
`;

    const html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>${escapeHtml(selectedDatabase)} / ${escapeHtml(selectedTable)} — schemaflow</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap">
<style>${wrapStyles}</style>
</head>
<body>
<header>
  <h1>${escapeHtml(selectedDatabase)} / ${escapeHtml(selectedTable)}</h1>
  <div class="crumb">schemaflow · ${currentActiveSection === 'data-flow' ? 'Data Flow' : 'Column Relationships'}</div>
</header>
<div class="canvas">${new XMLSerializer().serializeToString(clone)}</div>
</body>
</html>`;

    const blob = new Blob([html], { type: 'text/html' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${selectedDatabase}_${selectedTable}_${currentActiveSection}.html`;
    document.body.appendChild(a);
    a.click();
    setTimeout(() => {
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }, 100);
}

// Subset of diagram CSS that the exported HTML needs to stand alone.
function commonDiagramCss() {
    return `
.df-halo, .rel-halo { fill:none; stroke:var(--accent); stroke-opacity:.18; stroke-width:3; }
.df-card, .rel-card { fill:#fff; stroke:var(--border); stroke-width:1; }
.df-node.current .df-card, .rel-table.current .rel-card { stroke:var(--accent); stroke-width:1.6; }
.df-rail, .rel-rail { fill:var(--border); }
.df-node.engine-mergetree .df-rail, .rel-table.engine-mergetree .rel-rail { fill:var(--t-mergetree-fg); }
.df-node.engine-replicated .df-rail, .rel-table.engine-replicated .rel-rail { fill:var(--t-replicated-fg); }
.df-node.engine-distributed .df-rail, .rel-table.engine-distributed .rel-rail { fill:var(--t-distributed-fg); }
.df-node.engine-mview .df-rail, .rel-table.engine-mview .rel-rail { fill:var(--t-mview-fg); }
.df-node.engine-dictionary .df-rail, .rel-table.engine-dictionary .rel-rail { fill:var(--t-dictionary-fg); }
.df-node.current .df-rail, .rel-table.current .rel-rail { fill:var(--accent); }
.df-title { font: 600 11.5px 'JetBrains Mono', monospace; fill:var(--text); }
.df-sub   { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--faint); }
.df-meta  { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--muted); }
.df-pin   { fill:var(--accent); }
.df-pin-text { font: 700 9px 'JetBrains Mono', monospace; fill:#fff; }
.df-edge { fill:none; stroke:#cbd0d8; stroke-width:1; }
.df-edge.active { stroke:var(--accent); stroke-width:1.4; }
.rel-role { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--faint); }
.rel-name { font: 700 12px 'JetBrains Mono', monospace; fill:var(--text); }
.rel-engine { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--muted); }
.rel-divider { stroke:var(--border); stroke-width:.5; }
.rel-col-bg { fill:var(--bg); }
.rel-col-name { font: 400 11px 'JetBrains Mono', monospace; fill:var(--text); }
.rel-col-type { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--muted); }
.rel-edge { fill:none; stroke:var(--accent); stroke-opacity:.5; stroke-width:1.4; }
.rel-edge-label rect { fill:#fff; stroke:var(--border); }
.rel-edge-label text { font: 500 10px 'JetBrains Mono', monospace; fill:var(--accent); }
`;
}

// ─── Sidebar filter ──────────────────────────────────────────────────────
function setupSidebarFilter() {
    if (!sidebarFilterInput) return;

    let queryTimer = null;
    sidebarFilterInput.addEventListener('input', () => {
        clearTimeout(queryTimer);
        queryTimer = setTimeout(() => applySidebarFilter(sidebarFilterInput.value), 60);
    });
    sidebarFilterInput.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            sidebarFilterInput.value = '';
            applySidebarFilter('');
            sidebarFilterInput.blur();
        }
    });

    document.addEventListener('keydown', (e) => {
        // '/' focuses the sidebar filter, like in many code-search tools.
        if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
        const tag = (e.target.tagName || '').toLowerCase();
        if (tag === 'input' || tag === 'textarea' || e.target.isContentEditable) return;
        if (paletteEl && !paletteEl.hidden) return;
        e.preventDefault();
        sidebarFilterInput.focus();
        sidebarFilterInput.select();
    });
}

function applySidebarFilter(rawQuery) {
    const q = (rawQuery || '').trim().toLowerCase();
    const dbItems = databaseTree.querySelectorAll(':scope > li');
    let totalMatches = 0;

    // Allow matching by engine name too: e.g. "MaterializedView" or "MView".
    const engineKeyFromQuery = matchEngineKey(q);

    dbItems.forEach((dbItem) => {
        const dbSpan = dbItem.querySelector(':scope > .database');
        const ul = dbItem.querySelector(':scope > ul');
        if (!dbSpan || !ul) return;

        const dbName = (dbSpan.querySelector('.db-name')?.textContent || '').toLowerCase();
        const tableLis = ul.querySelectorAll(':scope > li.table');

        let dbHasMatch = false;
        let visibleTables = 0;
        tableLis.forEach((li) => {
            const tname = (li.dataset.table || '').toLowerCase();
            const tengine = (li.dataset.engine || '').toLowerCase();
            let matches = !q;
            if (q && !matches) matches = tname.includes(q) || dbName.includes(q);
            if (q && !matches && engineKeyFromQuery) matches = tengine === engineKeyFromQuery;
            li.classList.toggle('filter-hidden', !matches);
            if (matches) {
                dbHasMatch = true;
                visibleTables++;
            }
        });

        if (q) {
            dbItem.classList.toggle('filter-hidden', !dbHasMatch);
            if (dbHasMatch) {
                ul.style.display = 'block';
                dbSpan.classList.add('open');
            }
            totalMatches += visibleTables;
        } else {
            dbItem.classList.remove('filter-hidden');
        }
    });

    let emptyEl = databaseTree.querySelector('.filter-empty');
    if (q && totalMatches === 0) {
        if (!emptyEl) {
            emptyEl = document.createElement('li');
            emptyEl.className = 'filter-empty';
            emptyEl.textContent = 'no tables match';
            databaseTree.appendChild(emptyEl);
        }
    } else if (emptyEl) {
        emptyEl.remove();
    }
}

function matchEngineKey(qLower) {
    if (!qLower) return null;
    const aliases = {
        mergetree:   ['mergetree'],
        replicated:  ['replicated'],
        distributed: ['distributed'],
        mview:       ['materializedview', 'materialized view', 'mview', 'mv'],
        dictionary:  ['dictionary', 'dict'],
    };
    for (const [key, names] of Object.entries(aliases)) {
        if (names.some((n) => n === qLower)) return key;
    }
    return null;
}

// ─── Command palette ─────────────────────────────────────────────────────
let columnIndex = null;
let columnIndexLoading = null;
let paletteActiveIndex = 0;
let paletteResults = [];

function setupCommandPalette() {
    if (!paletteEl) return;

    if (paletteShortcutKbd) {
        paletteShortcutKbd.textContent = isMac() ? '⌘K' : 'Ctrl+K';
    }

    paletteBtn.addEventListener('click', openPalette);

    document.addEventListener('keydown', (e) => {
        // Cmd/Ctrl+K toggles
        if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
            e.preventDefault();
            if (paletteEl.hidden) openPalette();
            else closePalette();
            return;
        }
        if (paletteEl.hidden) return;
        if (e.key === 'Escape') {
            e.preventDefault();
            closePalette();
        } else if (e.key === 'ArrowDown') {
            e.preventDefault();
            movePaletteSelection(1);
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            movePaletteSelection(-1);
        } else if (e.key === 'Enter') {
            e.preventDefault();
            commitPaletteSelection();
        }
    });

    paletteEl.addEventListener('click', (e) => {
        if (e.target.hasAttribute('data-palette-close')) closePalette();
    });

    paletteInput.addEventListener('input', () => {
        renderPaletteResults(paletteInput.value);
    });
}

function isMac() {
    return /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '');
}

function openPalette() {
    paletteEl.hidden = false;
    paletteEl.setAttribute('aria-hidden', 'false');
    paletteInput.value = '';
    paletteActiveIndex = 0;
    ensureColumnIndex().then(() => {
        if (!paletteEl.hidden) renderPaletteResults('');
    });
    renderPaletteResults('');
    setTimeout(() => paletteInput.focus(), 0);
}

function closePalette() {
    paletteEl.hidden = true;
    paletteEl.setAttribute('aria-hidden', 'true');
    paletteResults = [];
    paletteActiveIndex = 0;
}

async function ensureColumnIndex() {
    if (columnIndex) return columnIndex;
    if (columnIndexLoading) return columnIndexLoading;
    columnIndexLoading = fetch('/api/columns')
        .then((r) => (r.ok ? r.json() : []))
        .then((data) => {
            columnIndex = Array.isArray(data) ? data : [];
            return columnIndex;
        })
        .catch(() => {
            columnIndex = [];
            return columnIndex;
        });
    return columnIndexLoading;
}

const ENGINE_PALETTE_ITEMS = Object.entries(ENGINE_TYPES).map(([key, v]) => ({
    kind: 'engine',
    engineKey: key,
    label: v.name,
}));

function getAllTables() {
    const items = [];
    document.querySelectorAll('.tree-view .table').forEach((el) => {
        const db = el.dataset.database;
        const t = el.dataset.table;
        const engine = el.dataset.engine || 'mergetree';
        if (db && t) items.push({ kind: 'table', db, table: t, engineKey: engine, id: `${db}.${t}` });
    });
    return items;
}

function scoreMatch(haystack, needle) {
    if (!needle) return 1;
    const h = haystack.toLowerCase();
    const n = needle.toLowerCase();
    if (h === n) return 1000;
    if (h.startsWith(n)) return 600 - (h.length - n.length); // prefer shorter haystacks
    const idx = h.indexOf(n);
    if (idx === -1) return 0;
    // substring match — prefer earlier positions and shorter haystacks
    return 300 - idx - (h.length - n.length) * 0.1;
}

function renderPaletteResults(rawQuery) {
    const q = (rawQuery || '').trim();
    paletteResultsEl.innerHTML = '';

    if (!q) {
        if (columnIndexLoading && !columnIndex) {
            const li = document.createElement('li');
            li.className = 'palette-loading';
            li.textContent = 'indexing columns…';
            paletteResultsEl.appendChild(li);
            return;
        }
        const li = document.createElement('li');
        li.className = 'palette-empty';
        li.textContent = 'Type to search tables, columns, or engines.';
        paletteResultsEl.appendChild(li);
        paletteResults = [];
        return;
    }

    const tables = getAllTables()
        .map((t) => ({ ...t, _score: Math.max(scoreMatch(t.table, q), scoreMatch(t.id, q) - 5) }))
        .filter((t) => t._score > 0)
        .sort((a, b) => b._score - a._score)
        .slice(0, 8);

    const cols = (columnIndex || [])
        .map((c) => ({ ...c, _score: scoreMatch(c.name, q) }))
        .filter((c) => c._score > 0)
        .sort((a, b) => b._score - a._score)
        .slice(0, 8);

    const engines = ENGINE_PALETTE_ITEMS
        .map((e) => ({ ...e, _score: scoreMatch(e.label, q) }))
        .filter((e) => e._score > 0)
        .sort((a, b) => b._score - a._score);

    paletteResults = [];

    if (tables.length) {
        paletteResultsEl.appendChild(groupLabel('Tables'));
        tables.forEach((t) => {
            paletteResults.push({ kind: 'table', db: t.db, table: t.table, engineKey: t.engineKey });
        });
        renderRows(tables.map((t) => ({
            kind: 'table',
            engineKey: t.engineKey,
            glyph: glyphFor(t.engineKey),
            label: highlight(t.table, q),
            sub: t.db,
            meta: ENGINE_TYPES[t.engineKey]?.name || '',
        })));
    }

    if (cols.length) {
        paletteResultsEl.appendChild(groupLabel('Columns'));
        cols.forEach((c) => {
            paletteResults.push({ kind: 'table', db: c.database, table: c.table });
        });
        renderRows(cols.map((c) => ({
            kind: 'column',
            glyph: '𝙓',
            label: highlight(c.name, q),
            sub: `${c.database}.${c.table}`,
            meta: simplifyType(c.type),
        })));
    }

    if (engines.length) {
        paletteResultsEl.appendChild(groupLabel('Engines'));
        engines.forEach((e) => {
            paletteResults.push({ kind: 'engine', engineKey: e.engineKey });
        });
        renderRows(engines.map((e) => ({
            kind: 'engine',
            glyph: '◌',
            label: highlight(e.label, q),
            sub: 'engine family',
        })));
    }

    if (!paletteResults.length) {
        const li = document.createElement('li');
        li.className = 'palette-empty';
        li.textContent = `No matches for “${q}”.`;
        paletteResultsEl.appendChild(li);
    }

    paletteActiveIndex = 0;
    updatePaletteActive();
}

function groupLabel(text) {
    const li = document.createElement('li');
    li.className = 'palette-group-label';
    li.textContent = text;
    return li;
}

function glyphFor(engineKey) {
    return ({
        mergetree:   '▤',
        replicated:  '◈',
        distributed: '⋈',
        mview:       '◐',
        dictionary:  '☱',
    })[engineKey] || '▤';
}

function simplifyType(type) {
    if (!type) return '';
    return type.length > 22 ? type.slice(0, 21) + '…' : type;
}

function highlight(label, q) {
    if (!q) return escapeHtml(label);
    const lower = label.toLowerCase();
    const needle = q.toLowerCase();
    const idx = lower.indexOf(needle);
    if (idx < 0) return escapeHtml(label);
    return (
        escapeHtml(label.slice(0, idx)) +
        '<span class="label-match">' +
        escapeHtml(label.slice(idx, idx + needle.length)) +
        '</span>' +
        escapeHtml(label.slice(idx + needle.length))
    );
}

function renderRows(rowSpecs) {
    rowSpecs.forEach((spec) => {
        const li = document.createElement('li');
        li.className = 'palette-row';
        li.setAttribute('data-kind', spec.kind);
        li.setAttribute('role', 'option');
        li.innerHTML = `
            <span class="row-glyph ${spec.engineKey ? `engine-${spec.engineKey}` : ''}">${escapeHtml(spec.glyph)}</span>
            <span class="row-label">${spec.label}${spec.sub ? `<span class="row-sub">${escapeHtml(spec.sub)}</span>` : ''}</span>
            <span class="row-meta">${escapeHtml(spec.meta || '')}</span>
        `;
        const indexInResults = paletteResultsEl.querySelectorAll('li.palette-row').length;
        li.addEventListener('mousemove', () => {
            if (paletteActiveIndex !== indexInResults) {
                paletteActiveIndex = indexInResults;
                updatePaletteActive();
            }
        });
        li.addEventListener('click', () => {
            paletteActiveIndex = indexInResults;
            commitPaletteSelection();
        });
        paletteResultsEl.appendChild(li);
    });
}

function updatePaletteActive() {
    const rows = paletteResultsEl.querySelectorAll('li.palette-row');
    rows.forEach((row, i) => row.classList.toggle('active', i === paletteActiveIndex));
    const active = rows[paletteActiveIndex];
    if (active) active.scrollIntoView({ block: 'nearest' });
}

function movePaletteSelection(delta) {
    const rows = paletteResultsEl.querySelectorAll('li.palette-row');
    if (!rows.length) return;
    paletteActiveIndex = (paletteActiveIndex + delta + rows.length) % rows.length;
    updatePaletteActive();
}

function commitPaletteSelection() {
    const result = paletteResults[paletteActiveIndex];
    if (!result) return;

    if (result.kind === 'table') {
        closePalette();
        selectTableByID(`${result.db}.${result.table}`);
    } else if (result.kind === 'engine') {
        closePalette();
        if (sidebarFilterInput) {
            const engineName = ENGINE_TYPES[result.engineKey]?.name || result.engineKey;
            sidebarFilterInput.value = engineName;
            applySidebarFilter(engineName);
            sidebarFilterInput.focus();
        }
    }
}
