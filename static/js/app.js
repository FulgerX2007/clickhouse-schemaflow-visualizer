// ─── Utilities ───────────────────────────────────────────────────────────
function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str == null ? '' : String(str);
    return div.innerHTML;
}

const ENGINE_TYPES = {
    mergetree:   { icon: 'fa-database',         name: 'MergeTree',         glyph: '▤' },
    replicated:  { icon: 'fa-circle-nodes',     name: 'Replicated',        glyph: '◈' },
    distributed: { icon: 'fa-diagram-project',  name: 'Distributed',       glyph: '⋈' },
    mview:       { icon: 'fa-eye',              name: 'MaterializedView',  glyph: '◐' },
    dictionary:  { icon: 'fa-book',             name: 'Dictionary',        glyph: '☱' },
};

function classifyEngine(engineName) {
    if (!engineName) return 'mergetree';
    const e = engineName.toLowerCase();
    if (e === 'distributed') return 'distributed';
    if (e === 'materializedview') return 'mview';
    if (e.startsWith('dictionary')) return 'dictionary';
    if (e.startsWith('replicated')) return 'replicated';
    return 'mergetree';
}

function classifyFromIconClassList(classList) {
    for (const cls of classList) {
        for (const [key, v] of Object.entries(ENGINE_TYPES)) {
            if (cls === v.icon) return key;
        }
    }
    return null;
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
const databaseTree       = document.getElementById('database-tree');
const refreshBtn         = document.getElementById('refresh-btn');
const currentSelection   = document.getElementById('current-selection');
const dataflowDiagram    = document.getElementById('dataflow-diagram');
const relationshipsDiagram = document.getElementById('relationships-diagram');
const exportHtmlBtn      = document.getElementById('export-html-btn');
const dbCountEl          = document.getElementById('db-count');

const dataflowZoomInBtn      = document.getElementById('dataflow-zoom-in-btn');
const dataflowZoomOutBtn     = document.getElementById('dataflow-zoom-out-btn');
const dataflowResetZoomBtn   = document.getElementById('dataflow-reset-zoom-btn');
const relationshipsZoomInBtn    = document.getElementById('relationships-zoom-in-btn');
const relationshipsZoomOutBtn   = document.getElementById('relationships-zoom-out-btn');
const relationshipsResetZoomBtn = document.getElementById('relationships-reset-zoom-btn');

const sectionTabs           = document.querySelectorAll('.section-tab');
const dataFlowSection       = document.getElementById('data-flow-section');
const relationshipsSection  = document.getElementById('relationships-section');

const tableDetailsContainer = document.querySelector('.table-details-container');
const tableDetailsContent   = document.getElementById('table-details');
const toggleTableDetailsBtn = document.getElementById('toggle-table-details');

// ─── State ───────────────────────────────────────────────────────────────
let databases = [];
let selectedDatabase = null;
let selectedTable = null;
let currentDataFlowSchema = null;
let currentRelationshipsSchema = null;
let currentActiveSection = 'data-flow';
let dataflowZoomLevel = 1;
let relationshipsZoomLevel = 1;

// ─── Mermaid theming ─────────────────────────────────────────────────────
const MERMAID_THEME = {
    startOnLoad: false,
    securityLevel: 'loose', // needed so embedded <i> icons in node labels render
    theme: 'base',
    themeVariables: {
        fontFamily: '"JetBrains Mono", ui-monospace, monospace',
        fontSize: '12px',
        background: '#fafafa',
        primaryColor: '#ffffff',
        primaryTextColor: '#0b0d12',
        primaryBorderColor: '#e8e8ea',
        lineColor: '#cbd0d8',
        secondaryColor: '#ffffff',
        tertiaryColor: '#ffffff',
        nodeBorder: '#e8e8ea',
        clusterBkg: '#ffffff',
        clusterBorder: '#e8e8ea',
        edgeLabelBackground: '#ffffff',
    },
    flowchart: {
        useMaxWidth: false,
        htmlLabels: true,
        curve: 'basis',
        padding: 16,
        nodeSpacing: 24,
        rankSpacing: 44,
    },
};

// ─── Boot ────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
    if (typeof mermaid !== 'undefined') {
        mermaid.initialize(MERMAID_THEME);
        console.log('Mermaid initialized');
    }

    loadDatabases();

    refreshBtn.addEventListener('click', loadDatabases);
    exportHtmlBtn.addEventListener('click', exportHtml);

    dataflowZoomInBtn.addEventListener('click', dataflowZoomIn);
    dataflowZoomOutBtn.addEventListener('click', dataflowZoomOut);
    dataflowResetZoomBtn.addEventListener('click', dataflowResetZoom);

    relationshipsZoomInBtn.addEventListener('click', relationshipsZoomIn);
    relationshipsZoomOutBtn.addEventListener('click', relationshipsZoomOut);
    relationshipsResetZoomBtn.addEventListener('click', relationshipsResetZoom);

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
});

// ─── Data loading ────────────────────────────────────────────────────────
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

// Map of "database.table" → engine key, populated as the sidebar renders.
// Used by the diagram decorator to apply per-engine rail colors.
const tableEngineMap = new Map();

function detectEngineFromIconHtml(html) {
    if (typeof html !== 'string') return null;
    if (html.includes('fa-database'))        return 'mergetree';
    if (html.includes('fa-circle-nodes'))    return 'replicated';
    if (html.includes('fa-diagram-project')) return 'distributed';
    if (html.includes('fa-eye'))             return 'mview';
    if (html.includes('fa-book'))            return 'dictionary';
    // The backend uses fa-table as a default fallback for non-matched engines
    // (e.g., SummingMergeTree, AggregatingMergeTree). Treat as MergeTree family.
    if (html.includes('fa-table'))           return 'mergetree';
    return null;
}

// Find an engine key by matching the rendered node text against the registered
// "db.table" map. Mermaid concatenates the label with the <small> metadata
// (e.g., "aggregated.cnx_6minRows: 358.2M …"), so we (a) chop off the metadata
// suffix at "Rows:" / "Size:" if present, then (b) try direct lookup, then
// (c) fall back to a longest-prefix scan of the map for cases that include
// trailing chars.
function lookupEngineByLabel(text) {
    const raw = (text || '').replace(/\s+/g, ' ').trim();
    if (!raw || !tableEngineMap.size) return null;

    const stripped = raw.split(/\s*(?:Rows:|Size:)/)[0].trim();
    if (tableEngineMap.has(stripped)) return tableEngineMap.get(stripped);

    let bestKey = null;
    for (const key of tableEngineMap.keys()) {
        if (!stripped.startsWith(key) && !raw.startsWith(key)) continue;
        if (!bestKey || key.length > bestKey.length) bestKey = key;
    }
    return bestKey ? tableEngineMap.get(bestKey) : null;
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

    const engineKey = detectEngineFromIconHtml(iconHtml);
    if (engineKey) {
        tableEngineMap.set(`${dbName}.${dbTable}`, engineKey);
        tableItem.dataset.engine = engineKey;
    }

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

    dataflowZoomLevel = 1;
    relationshipsZoomLevel = 1;

    renderBreadcrumb({ database: selectedDatabase, table: selectedTable, engine: null });

    await Promise.all([loadTableSchemas(), loadTableDetails(selectedDatabase, selectedTable)]);
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

// ─── Section switching + zoom ────────────────────────────────────────────
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

function dataflowZoomIn()    { dataflowZoomLevel = Math.min(dataflowZoomLevel + 0.1, 4); applyDataFlowZoom(); }
function dataflowZoomOut()   { dataflowZoomLevel = Math.max(dataflowZoomLevel - 0.1, 0.5); applyDataFlowZoom(); }
function dataflowResetZoom() { dataflowZoomLevel = 1; applyDataFlowZoom(); }
function applyDataFlowZoom() {
    if (dataflowDiagram) dataflowDiagram.style.transform = `scale(${dataflowZoomLevel})`;
}

function relationshipsZoomIn()    { relationshipsZoomLevel = Math.min(relationshipsZoomLevel + 0.1, 4); applyRelationshipsZoom(); }
function relationshipsZoomOut()   { relationshipsZoomLevel = Math.max(relationshipsZoomLevel - 0.1, 0.5); applyRelationshipsZoom(); }
function relationshipsResetZoom() { relationshipsZoomLevel = 1; applyRelationshipsZoom(); }
function applyRelationshipsZoom() {
    if (relationshipsDiagram) relationshipsDiagram.style.transform = `scale(${relationshipsZoomLevel})`;
}

// ─── Schema loading ──────────────────────────────────────────────────────
async function loadTableSchemas() {
    if (!selectedDatabase || !selectedTable) return;

    try {
        const [flowResp, relResp] = await Promise.all([
            fetch(`/api/schema/${selectedDatabase}/${selectedTable}`),
            fetch(`/api/relationships/${selectedDatabase}/${selectedTable}`),
        ]);
        if (!flowResp.ok) throw new Error(`Data Flow: HTTP ${flowResp.status}`);
        if (!relResp.ok) throw new Error(`Relationships: HTTP ${relResp.status}`);

        const flowData = await flowResp.json();
        const relData  = await relResp.json();

        currentDataFlowSchema      = flowData.schema;
        currentRelationshipsSchema = relData.schema;

        renderDataFlowSchema();
        renderRelationshipsSchema();
    } catch (error) {
        console.error('Error loading schemas:', error);
        showError('Failed to load table schemas.');
    }
}

function renderDataFlowSchema() {
    if (!currentDataFlowSchema) return;
    renderMermaidDiagramInContainer(dataflowDiagram, currentDataFlowSchema, 'dataflow');
}

function renderRelationshipsSchema() {
    if (!currentRelationshipsSchema) return;
    renderMermaidDiagramInContainer(relationshipsDiagram, currentRelationshipsSchema, 'relationships');
}

async function renderMermaidDiagramInContainer(container, schema, type) {
    if (!container || !schema) return;
    container.innerHTML = '';

    const mermaidContainer = document.createElement('div');
    mermaidContainer.className = 'mermaid';
    mermaidContainer.textContent = schema;
    container.appendChild(mermaidContainer);

    if (typeof mermaid === 'undefined') {
        const id = setInterval(() => {
            if (typeof mermaid !== 'undefined') {
                clearInterval(id);
                renderMermaidDiagramInContainer(container, schema, type);
            }
        }, 100);
        return;
    }

    try {
        mermaid.initialize(MERMAID_THEME);
        await mermaid.run({ nodes: [mermaidContainer] });
        decorateMermaidNodes(mermaidContainer);

        if (type === 'dataflow') {
            applyDataFlowZoom();
            setupMouseWheelZoomForSection(dataFlowSection, 'dataflow');
        } else {
            applyRelationshipsZoom();
            setupMouseWheelZoomForSection(relationshipsSection, 'relationships');
        }
    } catch (error) {
        console.error(`Error rendering ${type} diagram:`, error);
        showRawSchemaInContainer(container, schema);
    }
}

// ─── Colored rail injection (Variant A node style) ───────────────────────
// After Mermaid renders, walk each .node, detect engine type from the embedded
// Font Awesome icon, and prepend a 3px colored rect along the left edge.
function decorateMermaidNodes(root) {
    if (!root) return;
    const svg = root.querySelector('svg');
    if (!svg) return;

    // ensure SVG fills container width naturally
    svg.style.maxWidth = '100%';
    svg.removeAttribute('style');

    const nodes = svg.querySelectorAll('g.node');
    nodes.forEach((node) => {
        // Skip cluster/subgraph headers
        if (node.classList.contains('cluster')) return;

        // Detect engine type from contained <i> icon classes (only present in some
        // rendering paths) — fall back to a name lookup populated from the sidebar.
        let engineKey = null;
        node.querySelectorAll('i').forEach((iconEl) => {
            if (engineKey) return;
            const k = classifyFromIconClassList(iconEl.classList);
            if (k) engineKey = k;
        });
        if (!engineKey) {
            engineKey = lookupEngineByLabel(node.textContent || '');
        }

        // Find the principal shape (first rect/polygon/circle/ellipse in this node)
        const shape = node.querySelector('rect, polygon, circle, ellipse');
        if (!shape) return;

        // Detect "current" node by the backend's hardcoded orange fill (#FF6D00)
        const inlineStyle = shape.getAttribute('style') || '';
        const fillAttr = shape.getAttribute('fill') || '';
        const isCurrent =
            inlineStyle.includes('#FF6D00') || inlineStyle.includes('#ff6d00')
            || inlineStyle.includes('rgb(255, 109, 0)') || inlineStyle.includes('rgb(255,109,0)')
            || fillAttr.toLowerCase() === '#ff6d00';

        // Strip the backend's hardcoded orange/purple !important inline styles so our
        // theme can apply. We re-style "current" nodes via the .current class hook.
        if (isCurrent) {
            shape.removeAttribute('style');
            shape.removeAttribute('fill');
            shape.removeAttribute('stroke');
            // Backend also writes color:#FFFFFF on label foreignObject content — drop it
            // so labels stay visible on the white card.
            node.querySelectorAll('[style]').forEach((el) => {
                const s = el.getAttribute('style') || '';
                if (/#FF6D00|#ff6d00|#AA00FF|#aa00ff|#FFFFFF|rgb\(255,\s*255,\s*255\)/.test(s)) {
                    el.removeAttribute('style');
                }
            });
        } else if (inlineStyle.includes('!important')) {
            shape.removeAttribute('style');
        }

        // Apply engine + current class hooks (used by CSS rules)
        if (engineKey) node.classList.add(`engine-${engineKey}`);
        if (isCurrent) node.classList.add('current');

        // For rects only, inject the colored rail on the left edge
        if (shape.tagName.toLowerCase() === 'rect') {
            // Avoid double-inject on re-renders
            if (node.querySelector('.rail-bar')) return;

            const x = parseFloat(shape.getAttribute('x')) || 0;
            const y = parseFloat(shape.getAttribute('y')) || 0;
            const h = parseFloat(shape.getAttribute('height')) || 0;
            const rx = parseFloat(shape.getAttribute('rx')) || 0;

            const rail = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
            rail.setAttribute('class', 'rail-bar');
            rail.setAttribute('x', x);
            rail.setAttribute('y', y);
            rail.setAttribute('width', '3');
            rail.setAttribute('height', h);
            if (rx) rail.setAttribute('rx', Math.min(rx, 2));
            // Insert just after the principal shape so it draws above the body
            shape.parentNode.insertBefore(rail, shape.nextSibling);
        }
    });
}

function showRawSchemaInContainer(container, schema) {
    container.innerHTML = '';
    const raw = document.createElement('pre');
    raw.style.whiteSpace = 'pre-wrap';
    raw.style.fontFamily = 'inherit';
    raw.style.padding = '12px';
    raw.style.border = '1px solid var(--border)';
    raw.style.borderRadius = '6px';
    raw.style.background = '#fff';
    raw.textContent = schema;
    container.appendChild(raw);
}

// ─── Pan + wheel zoom ────────────────────────────────────────────────────
function setupMouseWheelZoomForSection(sectionContainer, sectionType) {
    if (!sectionContainer) return;
    const scrollArea = sectionContainer.querySelector('.diagram-scroll-area');
    if (!scrollArea || scrollArea.dataset.zoomBound === '1') {
        return;
    }
    scrollArea.dataset.zoomBound = '1';

    scrollArea.addEventListener('wheel', (event) => {
        if (!event.ctrlKey && !event.metaKey) return; // only zoom with Ctrl/Cmd
        event.preventDefault();
        const delta = event.deltaY;
        if (sectionType === 'dataflow') {
            if (delta < 0) dataflowZoomIn(); else dataflowZoomOut();
        } else {
            if (delta < 0) relationshipsZoomIn(); else relationshipsZoomOut();
        }
    }, { passive: false });

    let isDragging = false;
    let startX, startY, scrollLeft, scrollTop;

    scrollArea.addEventListener('mousedown', (e) => {
        if (e.button !== 0) return;
        isDragging = true;
        scrollArea.style.cursor = 'grabbing';
        startX = e.pageX - scrollArea.offsetLeft;
        startY = e.pageY - scrollArea.offsetTop;
        scrollLeft = scrollArea.scrollLeft;
        scrollTop = scrollArea.scrollTop;
        e.preventDefault();
    });
    scrollArea.addEventListener('mouseleave', () => { isDragging = false; scrollArea.style.cursor = 'grab'; });
    scrollArea.addEventListener('mouseup', () => { isDragging = false; scrollArea.style.cursor = 'grab'; });
    scrollArea.addEventListener('mousemove', (e) => {
        if (!isDragging) return;
        e.preventDefault();
        const x = e.pageX - scrollArea.offsetLeft;
        const y = e.pageY - scrollArea.offsetTop;
        scrollArea.scrollLeft = scrollLeft - (x - startX);
        scrollArea.scrollTop  = scrollTop  - (y - startY);
    });
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

// ─── Table details panel ────────────────────────────────────────────────
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
                            <td class="column-name" title="${escapeHtml(column.name)}">${escapeHtml(column.name)}</td>
                            <td><span class="column-type" title="${escapeHtml(column.type)}">${escapeHtml(column.type)}</span></td>
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
    // Lightweight non-blocking notice; alert kept as last-resort fallback
    alert(message);
}

// ─── Export HTML ─────────────────────────────────────────────────────────
function exportHtml() {
    const schema = currentActiveSection === 'data-flow'
        ? currentDataFlowSchema
        : currentRelationshipsSchema;

    if (!schema) {
        showError('No schema to export. Select a table first.');
        return;
    }

    const html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>${escapeHtml(selectedDatabase || '')} / ${escapeHtml(selectedTable || '')} — schemaflow</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap">
<script src="https://cdn.jsdelivr.net/npm/mermaid@11.6.0/dist/mermaid.min.js" crossorigin="anonymous" defer><\/script>
<style>
  :root { --accent:#3b5bdb; --border:#e8e8ea; --bg:#fafafa; --text:#0b0d12; --muted:#6b7280; }
  *, *::before, *::after { box-sizing: border-box; }
  body { margin:0; font-family:'JetBrains Mono', ui-monospace, monospace; background:var(--bg); color:var(--text); }
  header { padding:14px 20px; border-bottom:1px solid var(--border); background:#fff; }
  header h1 { margin:0; font-size:13px; font-weight:600; }
  header .crumb { font-size:11px; color:var(--muted); margin-top:4px; }
  .canvas { padding:24px; min-height:calc(100vh - 56px); background-image: radial-gradient(circle, #d8dadf 1px, transparent 1px); background-size: 20px 20px; display:flex; justify-content:center; }
  .mermaid { font-family: inherit; }
  .mermaid .node rect { fill:#fff !important; stroke:var(--border) !important; }
  .mermaid .edgePath .path { stroke:#cbd0d8 !important; }
</style>
</head>
<body>
<header>
  <h1>${escapeHtml(selectedDatabase)} / ${escapeHtml(selectedTable)}</h1>
  <div class="crumb">schemaflow · ${currentActiveSection === 'data-flow' ? 'Data Flow' : 'Column Relationships'}</div>
</header>
<div class="canvas"><pre class="mermaid">${escapeHtml(schema)}</pre></div>
<script>
document.addEventListener('DOMContentLoaded', function() {
  if (typeof mermaid === 'undefined') return;
  mermaid.initialize({
    startOnLoad: true,
    theme: 'base',
    themeVariables: {
      fontFamily: '"JetBrains Mono", ui-monospace, monospace',
      primaryColor: '#ffffff', primaryTextColor: '#0b0d12',
      primaryBorderColor: '#e8e8ea', lineColor: '#cbd0d8'
    },
    flowchart: { curve: 'basis', nodeSpacing: 24, rankSpacing: 44 }
  });
});
<\/script>
</body>
</html>`;

    const blob = new Blob([html], { type: 'text/html' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${selectedDatabase}_${selectedTable}_schema.html`;
    document.body.appendChild(a);
    a.click();
    setTimeout(() => {
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }, 100);
}
