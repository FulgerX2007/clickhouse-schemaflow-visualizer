// ─── Utilities ───────────────────────────────────────────────────────────
function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str == null ? '' : String(str);
    return div.innerHTML;
}

// ─── Grafana dashboard usage ─────────────────────────────────────────────
// The whole feature is render-gated on this status. When Grafana is not
// configured the server answers state:"disabled" and nothing below ever creates
// a DOM node, so the page carries no trace of the feature at all.
let grafanaStatus = null;
let currentUsage = null;

const VERDICT_LABELS = {
    'used': 'used',
    'unused': 'unused',
    'unknown': 'unknown',
    'no-coverage': 'no data',
};

// What each verdict licenses. Only "unused" means safe to drop.
const VERDICT_TITLES = {
    'used': 'Read by a dashboard, by lineage, or by ClickHouse itself.',
    'unused': 'The table is read, every query reading it was fully understood, and none name this column.',
    'unknown': 'The table is read but a query touching it could not be enumerated. No claim is made.',
    'no-coverage': 'Nothing observed reads this table. That is not evidence the column is dead.',
};

async function loadGrafanaStatus() {
    try {
        const response = await fetch('/api/grafana/status');
        if (!response.ok) return;
        grafanaStatus = await response.json();
        buildDashboardUsageSection();
        buildUnusedSection();
        // The remembered section may only now exist.
        restoreActiveSection();
        // A table may have been selected while the status was still in flight.
        renderDashboardUsage();
        if (grafanaStatus.state === 'scanning') pollGrafanaStatus(0);
    } catch (error) {
        console.warn('Grafana status unavailable:', error);
        grafanaStatus = null;
    }
}

// grafanaVisible gates every affordance. "disabled" hides the feature entirely;
// "error" and "scanning" still render, with a banner, because a Grafana that is
// configured but unreachable must never look like one where nothing is used.
function grafanaVisible() {
    return Boolean(grafanaStatus && grafanaStatus.state && grafanaStatus.state !== 'disabled');
}

function grafanaReady() {
    return Boolean(grafanaStatus && grafanaStatus.state === 'ok');
}

async function loadTableUsage(database, table) {
    if (!grafanaVisible()) return null;
    try {
        const response = await fetch(`/api/grafana/usage/${encodeURIComponent(database)}/${encodeURIComponent(table)}`);
        if (!response.ok) return null;
        const payload = await response.json();
        syncGrafanaState(payload);
        // The server withholds the payload unless the scan completed.
        return payload.state === 'ok' ? payload.usage : null;
    } catch (error) {
        console.warn('Grafana usage unavailable:', error);
        return null;
    }
}

// syncGrafanaState refreshes the cached status from any endpoint that carries
// one. loadGrafanaStatus runs once at boot, and a first scan of a few hundred
// dashboards easily outlives it — without this the cached state stays
// "scanning" for the life of the page and every view gated on it says the data
// is not ready long after it is.
function syncGrafanaState(payload) {
    if (!payload || !payload.state || !grafanaStatus) return;
    if (payload.state === grafanaStatus.state) return;
    grafanaStatus = { ...grafanaStatus, ...payload };
    delete grafanaStatus.usage;
    delete grafanaStatus.report;
}

// pollGrafanaStatus watches a scan through to completion so the UI recovers on
// its own. Bounded: a scan that has not finished in a few minutes is a problem
// to report, not to keep polling.
let grafanaPollTimer = null;
function pollGrafanaStatus(attempt) {
    if (grafanaPollTimer || attempt > 40) return;
    grafanaPollTimer = setTimeout(async () => {
        grafanaPollTimer = null;
        try {
            const response = await fetch('/api/grafana/status');
            if (!response.ok) return;
            const payload = await response.json();
            const changed = !grafanaStatus || payload.state !== grafanaStatus.state;
            grafanaStatus = payload;
            if (payload.state === 'scanning') {
                pollGrafanaStatus(attempt + 1);
            } else if (changed) {
                // The scan landed: re-fetch this table's usage and redraw.
                if (selectedDatabase && selectedTable) {
                    currentUsage = await loadTableUsage(selectedDatabase, selectedTable);
                }
                renderDashboardUsage();
                if (currentActiveSection === 'unused-columns') loadUnusedReport();
            }
        } catch (error) {
            console.warn('Grafana status poll failed:', error);
        }
    }, 5000);
}

// ─── Dashboard-usage diagram ─────────────────────────────────────────────
//
// The bipartite view of the same evidence the inspector lists per column:
// the selected table on the left, the Grafana dashboards reading it on the
// right, one line per (column, panel) pair. Like the unused-columns report it
// exists in the DOM only when Grafana is configured.

let dashboardUsagePanzoom = null;
let dashboardHideUnconnected = false;
// null means "let the renderer decide from the panel count". Once the user
// touches the toggle their choice sticks for the session, because a control that
// silently re-decides itself on the next table reads as broken.
let dashboardPanelMode = null;

function buildDashboardUsageSection() {
    if (!grafanaVisible() || document.getElementById('dashboard-usage-section')) return;

    const tabs = document.querySelector('.section-tabs');
    const container = document.querySelector('.schema-sections');
    if (!tabs || !container) return;

    const tab = document.createElement('button');
    tab.className = 'section-tab';
    tab.dataset.section = 'dashboard-usage';
    const icon = document.createElement('i');
    icon.className = 'fa-solid fa-chart-line';
    tab.appendChild(icon);
    tab.appendChild(document.createTextNode(' Dashboards'));
    tab.addEventListener('click', () => switchSection('dashboard-usage'));
    tabs.appendChild(tab);

    const section = document.createElement('div');
    section.className = 'schema-container hidden';
    section.id = 'dashboard-usage-section';
    section.innerHTML = `
        <div class="section-header">
            <div class="section-header-text">
                <h3>Dashboard usage</h3>
                <p class="section-description">Which Grafana dashboards read this table, and which column each panel touches.</p>
            </div>
            <div class="view-controls">
                <label class="du-toggle" title="Show each panel as its own row instead of one row per dashboard. Off by default above a dozen panels, where the full picture does not fit.">
                    <input type="checkbox" id="du-panel-mode"> panels
                </label>
                <label class="du-toggle" title="Hide columns no query names. Columns kept alive only through a materialized view are hidden too — they carry no line.">
                    <input type="checkbox" id="du-hide-unconnected"> read only
                </label>
                <button id="dashboards-zoom-out-btn" title="Zoom out"><i>−</i></button>
                <button id="dashboards-reset-zoom-btn" title="Reset zoom"><i>⤢</i></button>
                <button id="dashboards-zoom-in-btn" title="Zoom in"><i>+</i></button>
            </div>
        </div>
        <p id="dashboards-stats" class="du-stats" hidden></p>
        <div class="diagram-scroll-area" id="dashboards-scroll">
            <div id="dashboards-diagram" class="diagram-canvas"></div>
        </div>
    `;
    container.appendChild(section);

    section.querySelector('#du-hide-unconnected').addEventListener('change', (event) => {
        dashboardHideUnconnected = event.target.checked;
        renderDashboardUsage();
    });
    section.querySelector('#du-panel-mode').addEventListener('change', (event) => {
        dashboardPanelMode = event.target.checked;
        renderDashboardUsage();
    });
    section.querySelector('#dashboards-zoom-in-btn')
        .addEventListener('click', () => dashboardUsagePanzoom && dashboardUsagePanzoom.zoomIn());
    section.querySelector('#dashboards-zoom-out-btn')
        .addEventListener('click', () => dashboardUsagePanzoom && dashboardUsagePanzoom.zoomOut());
    section.querySelector('#dashboards-reset-zoom-btn')
        .addEventListener('click', () => dashboardUsagePanzoom && dashboardUsagePanzoom.reset());
}

// renderDashboardUsage draws from currentUsage, which loadTableDetails already
// fetched. It is a no-op until both the section and a selection exist, so it is
// safe to call from the selection path, the Grafana-status path and the tab.
function renderDashboardUsage() {
    const container = document.getElementById('dashboards-diagram');
    if (!container || !window.SchemaDiagram) return;

    dashboardUsagePanzoom = null;

    if (!selectedDatabase || !selectedTable) {
        renderDiagramNotice(container, 'Select a table to see the dashboards that read it.');
        return;
    }
    // Without a completed scan there is no evidence, only the absence of it —
    // an empty diagram here would read as "no dashboard uses this table".
    if (!grafanaReady()) {
        if (grafanaStatus && grafanaStatus.state === 'scanning') pollGrafanaStatus(0);
        const detail = grafanaStatus && grafanaStatus.state === 'scanning'
            ? 'Scanning Grafana dashboards — this view will fill in on its own when the scan lands.'
            : `Grafana is configured but unreachable, so dashboard usage is unknown — not absent: ${
                (grafanaStatus && (grafanaStatus.error || grafanaStatus.reason)) || 'unknown error'}`;
        renderDiagramNotice(container, detail);
        return;
    }
    if (!currentUsage) {
        renderDiagramNotice(container, 'No dashboard usage was returned for this table.');
        return;
    }

    const result = window.SchemaDiagram.renderDashboardUsage(container, currentUsage, {
        engineType: classifyEngine(currentUsage.engine),
        hideUnconnected: dashboardHideUnconnected,
        panelMode: dashboardPanelMode,
        // The renderer never builds a URL: panelURL owns the scheme check, so a
        // dashboard_url out of Grafana cannot become a javascript: navigation.
        onOpen: (ref) => {
            const url = panelURL(ref);
            if (url) window.open(url, '_blank', 'noopener,noreferrer');
        },
    });
    dashboardUsagePanzoom = result ? result.panzoom : null;
    // Fitting the whole picture into the pane is right until the picture is far
    // taller than it is wide — per-panel rows on a heavily-read table reach five
    // times the pane's height, and fitting that means an 18% scale nobody can
    // read. Past the threshold the canvas goes back to its natural aspect and
    // the pane scrolls, which at least stays legible.
    container.classList.toggle('tall',
        Boolean(result && result.height > result.width * 1.6));
    renderDashboardStats(result && result.stats);
}

// renderDashboardStats states in words what the picture cannot be counted off.
// A table read by 26 dashboards draws several hundred lines; the totals, and
// which of them are not drawn, have to be legible regardless.
function renderDashboardStats(stats) {
    const target = document.getElementById('dashboards-stats');
    const toggle = document.getElementById('du-panel-mode');
    if (!target) return;
    if (!stats) {
        target.hidden = true;
        return;
    }
    if (toggle) toggle.checked = stats.panelMode;

    const parts = [
        `${stats.dashboards} dashboard${stats.dashboards === 1 ? '' : 's'}`,
        `${stats.panels} panel${stats.panels === 1 ? '' : 's'}`,
        `${stats.columnsNamed} of ${stats.columnsTotal} columns named by a query`,
    ];
    // Stated separately and never drawn as a line: these columns are in use, but
    // what keeps them alive is a view reading the whole table, not a dashboard
    // naming them. Leaving the count out would invite exactly the wrong
    // conclusion from the absence of a line.
    if (stats.columnsLineage) {
        parts.push(`${stats.columnsLineage} more kept alive through a materialized view`);
    }
    if (stats.hidden > 0) {
        parts.push(`${stats.hidden} dashboard${stats.hidden === 1 ? '' : 's'} not drawn`);
    }
    if (!stats.panelMode && stats.autoPanelMode && stats.panels > stats.dashboards) {
        parts.push('grouped by dashboard — tick "panels" for per-panel rows');
    }
    target.textContent = parts.join(' · ');
    target.hidden = false;
}

function renderDiagramNotice(container, message) {
    renderDashboardStats(null);
    container.innerHTML = '';
    const note = document.createElement('div');
    note.className = 'diagram-empty';
    note.textContent = message;
    container.appendChild(note);
}

// ─── Unused-columns report ───────────────────────────────────────────────

let unusedReport = null;
let unusedSort = { key: 'table', ascending: true };

// buildUnusedSection creates the nav tab and the section, and is only ever
// called when Grafana is configured. Nothing here exists in the DOM otherwise.
function buildUnusedSection() {
    if (!grafanaVisible() || document.getElementById('unused-columns-section')) return;

    const tabs = document.querySelector('.section-tabs');
    const container = document.querySelector('.schema-sections');
    if (!tabs || !container) return;

    const tab = document.createElement('button');
    tab.className = 'section-tab';
    tab.dataset.section = 'unused-columns';
    const icon = document.createElement('i');
    icon.className = 'fa-solid fa-broom';
    tab.appendChild(icon);
    tab.appendChild(document.createTextNode(' Unused columns'));
    tab.addEventListener('click', () => switchSection('unused-columns'));
    tabs.appendChild(tab);

    const section = document.createElement('div');
    section.className = 'schema-container hidden';
    section.id = 'unused-columns-section';
    section.innerHTML = `
        <div class="section-header">
            <div class="section-header-text">
                <h3>Unused columns</h3>
                <p class="section-description">Columns no Grafana dashboard reads, directly or through lineage.</p>
            </div>
            <div class="view-controls">
                <select id="unused-database-filter" title="Filter by database"><option value="">all databases</option></select>
                <select id="unused-verdict-filter" title="Filter by verdict">
                    <option value="unused">unused</option>
                    <option value="unknown">unknown</option>
                    <option value="no-coverage">no data</option>
                    <option value="used">used</option>
                    <option value="">all</option>
                </select>
                <button id="unused-refresh-btn" title="Re-scan Grafana">↻</button>
            </div>
        </div>
        <div class="unused-scroll-area">
            <div id="unused-report" class="unused-report"></div>
        </div>
    `;
    container.appendChild(section);

    section.querySelector('#unused-verdict-filter').addEventListener('change', loadUnusedReport);
    section.querySelector('#unused-database-filter').addEventListener('change', loadUnusedReport);
    section.querySelector('#unused-refresh-btn').addEventListener('click', refreshGrafanaScan);
}

async function refreshGrafanaScan() {
    const button = document.getElementById('unused-refresh-btn');
    if (button) button.disabled = true;
    try {
        const response = await fetch('/api/grafana/refresh', { method: 'POST' });
        if (response.ok) grafanaStatus = await response.json();
    } catch (error) {
        console.warn('Grafana refresh failed:', error);
    } finally {
        if (button) button.disabled = false;
    }
    await loadUnusedReport();
}

async function loadUnusedReport() {
    const target = document.getElementById('unused-report');
    if (!target) return;

    const database = document.getElementById('unused-database-filter')?.value || '';
    const verdict = document.getElementById('unused-verdict-filter')?.value ?? 'unused';

    const params = new URLSearchParams();
    if (database) params.set('database', database);
    if (verdict) params.set('verdict', verdict);

    try {
        const response = await fetch(`/api/grafana/unused?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const payload = await response.json();
        if (payload.state !== 'ok') {
            renderUnusedUnavailable(target, payload);
            return;
        }
        unusedReport = payload.report;
        renderUnusedReport(target, payload.report);
    } catch (error) {
        console.error('Error loading the unused-columns report:', error);
        target.textContent = 'Failed to load the report.';
    }
}

function renderUnusedUnavailable(target, payload) {
    target.innerHTML = '';
    const note = document.createElement('p');
    note.className = payload.state === 'scanning' ? 'usage-banner scanning' : 'usage-banner error';
    note.textContent = payload.state === 'scanning'
        ? 'Scanning Grafana dashboards — the report is not available yet.'
        : `Grafana usage is unavailable, so no column can be judged: ${payload.error || payload.reason || payload.state}`;
    target.appendChild(note);
}

function renderUnusedReport(target, report) {
    target.innerHTML = '';
    populateDatabaseFilter(report);

    target.appendChild(unusedTotals(report.totals));
    target.appendChild(unusedLegend());

    (report.caveats || []).forEach((caveat) => {
        const note = document.createElement('p');
        note.className = 'unused-caveat';
        note.textContent = caveat;
        target.appendChild(note);
    });

    if (!report.rows || !report.rows.length) {
        const empty = document.createElement('p');
        empty.className = 'unused-empty';
        empty.textContent = 'No columns match this filter.';
        target.appendChild(empty);
        return;
    }

    target.appendChild(unusedTable(report.rows));
}

function populateDatabaseFilter(report) {
    const select = document.getElementById('unused-database-filter');
    if (!select || select.dataset.filled === 'true') return;

    const databases = [...new Set((report.rows || []).map((row) => row.database))].sort();
    databases.forEach((database) => {
        const option = document.createElement('option');
        option.value = database;
        option.textContent = database;
        select.appendChild(option);
    });
    if (databases.length) select.dataset.filled = 'true';
}

function unusedTotals(totals) {
    const bar = document.createElement('div');
    bar.className = 'unused-totals';
    [
        ['unused', totals.unused, 'safe to drop'],
        ['used', totals.used, 'read by something'],
        ['unknown', totals.unknown, 'could not be judged'],
        ['no-coverage', totals.no_coverage, 'nothing reads the table'],
    ].forEach(([verdict, count, description]) => {
        const chip = document.createElement('span');
        chip.className = `verdict-badge verdict-${verdict}`;
        chip.textContent = `${count} ${VERDICT_LABELS[verdict] || verdict}`;
        chip.title = description;
        bar.appendChild(chip);
    });
    return bar;
}

// unusedLegend states plainly what each verdict licenses. Three of the four
// states mean "do not touch this", and a reader who misses that drops a live
// column.
function unusedLegend() {
    const legend = document.createElement('p');
    legend.className = 'unused-legend';
    legend.textContent = 'Only "unused" means safe to drop — and only as far as Grafana can see. '
        + '"unknown" means a query could not be read, "no data" means nothing reads the table at all, '
        + 'and key columns count as used because ClickHouse refuses to drop them. '
        + 'The dashboard count is per table, not per column: an unused column normally sits in a '
        + 'table several dashboards read, which is exactly why its absence from every query is meaningful.';
    return legend;
}

const UNUSED_COLUMNS = [
    { key: 'database', label: 'Database' },
    { key: 'table', label: 'Table' },
    { key: 'column', label: 'Column' },
    { key: 'type', label: 'Type' },
    { key: 'verdict', label: 'Verdict' },
    { key: 'reason', label: 'Reason' },
    // Counts dashboards reading the TABLE, not this column. Labelled "Dashboards"
    // it read as a contradiction — "unused" beside "4" looks like the verdict is
    // backwards — so the header says whose count it is.
    {
        key: 'dashboards',
        label: 'Dashboards on table',
        title: 'How many dashboards read this table at all — not this column.\n'
             + 'A column is only ever called unused when its table IS read; if nothing '
             + 'read the table the verdict would be "no data" instead.',
    },
];

function unusedTable(rows) {
    const sorted = [...rows].sort((a, b) => {
        const left = a[unusedSort.key] ?? '';
        const right = b[unusedSort.key] ?? '';
        const order = typeof left === 'number' && typeof right === 'number'
            ? left - right
            : String(left).localeCompare(String(right));
        return unusedSort.ascending ? order : -order;
    });

    const table = document.createElement('table');
    table.className = 'unused-table';

    const head = document.createElement('thead');
    const headRow = document.createElement('tr');
    UNUSED_COLUMNS.forEach((column) => {
        const cell = document.createElement('th');
        cell.textContent = column.label;
        cell.className = 'sortable';
        if (column.title) cell.title = column.title;
        if (unusedSort.key === column.key) cell.classList.add(unusedSort.ascending ? 'asc' : 'desc');
        cell.addEventListener('click', () => {
            unusedSort = {
                key: column.key,
                ascending: unusedSort.key === column.key ? !unusedSort.ascending : true,
            };
            const target = document.getElementById('unused-report');
            if (target && unusedReport) renderUnusedReport(target, unusedReport);
        });
        headRow.appendChild(cell);
    });
    head.appendChild(headRow);
    table.appendChild(head);

    const body = document.createElement('tbody');
    sorted.forEach((row) => {
        const tr = document.createElement('tr');
        tr.className = 'unused-row';
        tr.title = 'Open this table in the inspector';
        tr.addEventListener('click', () => selectTableByID(`${row.database}.${row.table}`));

        UNUSED_COLUMNS.forEach((column) => {
            const cell = document.createElement('td');
            if (column.key === 'verdict') {
                const badge = document.createElement('span');
                badge.className = `verdict-badge verdict-${row.verdict}`;
                badge.textContent = VERDICT_LABELS[row.verdict] || row.verdict;
                badge.title = VERDICT_TITLES[row.verdict] || '';
                cell.appendChild(badge);
            } else {
                cell.textContent = row[column.key] === undefined || row[column.key] === '' ? '—' : String(row[column.key]);
                if (column.key === 'type' || column.key === 'reason') cell.className = 'muted';
                if (column.title) {
                    cell.className = 'muted';
                    cell.title = column.title;
                }
            }
            tr.appendChild(cell);
        });
        body.appendChild(tr);
    });
    table.appendChild(body);
    return table;
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
    loadGrafanaStatus();

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

    restoreActiveSection();

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
// sectionElements maps a section name to its container. The dashboard-usage and
// unused-columns entries only exist when Grafana is configured, so an absent key
// is normal rather than an error.
function sectionElements() {
    const sections = {
        'data-flow': dataFlowSection,
        'relationships': relationshipsSection,
    };
    const dashboards = document.getElementById('dashboard-usage-section');
    if (dashboards) sections['dashboard-usage'] = dashboards;
    const unused = document.getElementById('unused-columns-section');
    if (unused) sections['unused-columns'] = unused;
    return sections;
}

// switchSection handles any number of sections. It was a two-way if/else, which
// would have silently ignored a third section and left the old one on screen.
function switchSection(sectionName) {
    const sections = sectionElements();
    const resolved = sections[sectionName] ? sectionName : 'data-flow';

    // Re-queried rather than cached: the unused-columns tab is added after load.
    document.querySelectorAll('.section-tab').forEach((tab) => {
        tab.classList.toggle('active', tab.dataset.section === resolved);
    });
    Object.entries(sections).forEach(([name, element]) => {
        element.classList.toggle('hidden', name !== resolved);
    });

    currentActiveSection = resolved;
    // Only a section the caller could actually reach is remembered. Persisting
    // the fallback would erase a saved preference for a section that has not
    // been created yet — the unused-columns tab is added after an async fetch.
    if (resolved === sectionName) localStorage.setItem('activeSection', sectionName);
    if (resolved === 'unused-columns') loadUnusedReport();
    if (resolved === 'dashboard-usage') renderDashboardUsage();
}

// restoreActiveSection re-applies the remembered section. It runs once at load
// and again once the Grafana status resolves, because the section it names may
// not exist until then.
function restoreActiveSection() {
    const saved = localStorage.getItem('activeSection');
    if (saved && saved !== currentActiveSection && sectionElements()[saved]) {
        switchSection(saved);
    }
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
        currentUsage = await loadTableUsage(database, table);
        renderTableDetails(details, currentUsage);
        renderDashboardUsage();
        renderBreadcrumb({ database: details.database, table: details.name, engine: details.engine });
    } catch (error) {
        console.error('Error loading table details:', error);
        showTableDetailsError('Failed to load table details.');
    }
}

function renderTableDetails(details, usage) {
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
                ${details.primary_key ? `
                <span class="table-info-label">primary key</span>
                <span class="table-info-value code">${escapeHtml(details.primary_key)}</span>
                ` : ''}
                ${details.sorting_key ? `
                <span class="table-info-label">order by</span>
                <span class="table-info-value code">${escapeHtml(details.sorting_key)}</span>
                ` : ''}
                ${details.partition_key ? `
                <span class="table-info-label">partition by</span>
                <span class="table-info-value code">${escapeHtml(details.partition_key)}</span>
                ` : ''}
                <span class="table-info-label">rows</span>
                <span class="table-info-value numeric">${details.total_rows != null ? formatRows(details.total_rows) : '—'}</span>
                <span class="table-info-label">size</span>
                <span class="table-info-value">${formatBytes(details.total_bytes)}</span>
            </div>
        </div>

        <div class="columns-section">
            <h4>columns <span class="col-count">${cols.length} total</span>${usageSummary(usage)}</h4>
            ${grafanaBanner()}
            <table class="columns-table">
                <thead>
                    <tr><th>Name</th><th>Type</th></tr>
                </thead>
                <tbody>
                    ${cols.map((column, index) => `
                        <tr data-column-index="${index}">
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
    decorateColumnsWithUsage(cols, usage);
}

// usageSummary is a compact count beside the columns heading.
function usageSummary(usage) {
    if (!usage) return '';
    const parts = [];
    if (usage.unused_count) parts.push(`${usage.unused_count} unused`);
    if (usage.unknown_count) parts.push(`${usage.unknown_count} unknown`);
    if (!parts.length) return '';
    return ` <span class="col-count usage-summary">${escapeHtml(parts.join(' · '))}</span>`;
}

// grafanaBanner explains a Grafana that is configured but not answering.
// Without it a broken integration would look exactly like a schema nothing reads.
function grafanaBanner() {
    if (!grafanaVisible() || grafanaReady()) return '';
    if (grafanaStatus.state === 'scanning') {
        return '<p class="usage-banner scanning">Scanning Grafana dashboards — column usage is not available yet.</p>';
    }
    const detail = grafanaStatus.error || grafanaStatus.reason || 'unknown error';
    return `<p class="usage-banner error">Grafana is configured but unreachable, so column usage is
        <strong>unknown</strong> — not unused. ${escapeHtml(detail)}</p>`;
}

// decorateColumnsWithUsage adds the verdict badges and the evidence rows.
//
// Built with createElement rather than an innerHTML template on purpose: the
// evidence carries dashboard titles and URLs from Grafana, and escapeHtml is a
// textContent round-trip that does not escape the double quote, which makes it
// unsafe in an href="…" attribute.
function decorateColumnsWithUsage(cols, usage) {
    if (!usage || !Array.isArray(usage.columns)) return;

    const verdicts = new Map();
    usage.columns.forEach((entry) => verdicts.set(entry.column, entry));

    const header = tableDetailsContent.querySelector('.columns-table thead tr');
    if (header) {
        const cell = document.createElement('th');
        cell.textContent = 'Usage';
        cell.className = 'usage-header';
        header.appendChild(cell);
    }

    tableDetailsContent.querySelectorAll('.columns-table tbody tr').forEach((row) => {
        const column = cols[Number(row.dataset.columnIndex)];
        const entry = column && verdicts.get(column.name);
        const cell = document.createElement('td');
        cell.className = 'usage-cell';
        if (!entry) {
            cell.textContent = '—';
            row.appendChild(cell);
            return;
        }

        cell.appendChild(verdictBadge(entry));
        row.appendChild(cell);

        const evidence = usageEvidence(entry);
        if (!evidence) return;

        const evidenceRow = document.createElement('tr');
        evidenceRow.className = 'usage-evidence-row';
        evidenceRow.hidden = true;
        const evidenceCell = document.createElement('td');
        evidenceCell.colSpan = 2;
        evidenceCell.appendChild(evidence);
        evidenceRow.appendChild(evidenceCell);
        row.after(evidenceRow);

        cell.classList.add('expandable');
        cell.addEventListener('click', () => {
            evidenceRow.hidden = !evidenceRow.hidden;
            cell.classList.toggle('expanded', !evidenceRow.hidden);
        });
    });
}

// allRefs flattens a column's evidence.
function allRefs(entry) {
    return [...(entry.direct || []), ...(entry.derived || [])];
}

// groupRefsByDashboard collapses panel-level references into one entry per
// dashboard. A single dashboard can read a column from dozens of panels, and
// through a materialized view every panel of it contributes a reference.
function groupRefsByDashboard(refs) {
    const groups = new Map();
    refs.forEach((ref) => {
        const key = ref.dashboard_uid || ref.dashboard_title || '?';
        if (!groups.has(key)) {
            groups.set(key, { dashboard: ref, panels: [], via: new Set(), heuristic: false });
        }
        const group = groups.get(key);
        group.panels.push(ref);
        if (ref.via) group.via.add(ref.via);
        if (ref.confidence === 'heuristic') group.heuristic = true;
    });
    return [...groups.values()];
}

function verdictBadge(entry) {
    const badge = document.createElement('span');
    badge.className = `verdict-badge verdict-${entry.verdict}`;
    const refs = allRefs(entry);
    const groups = groupRefsByDashboard(refs);
    badge.textContent = VERDICT_LABELS[entry.verdict] || entry.verdict;
    // Count dashboards, not references: one dashboard reading a column from
    // thirty panels is still one dashboard, and "(30)" would read as thirty.
    if (groups.length > 0) badge.textContent += ` (${groups.length})`;

    const title = [VERDICT_TITLES[entry.verdict] || ''];
    if (groups.length > 0) {
        const panels = refs.length;
        title.push(`Read by ${groups.length} dashboard${groups.length === 1 ? '' : 's'}`
            + ` across ${panels} panel quer${panels === 1 ? 'y' : 'ies'}.`);
    }
    if (entry.reason) title.push(`Reason: ${entry.reason}`);
    badge.title = title.filter(Boolean).join('\n');
    return badge;
}

// usageEvidence lists the dashboards behind a verdict, with the SQL that proves
// it. Every node is created rather than templated.
function usageEvidence(entry) {
    const refs = allRefs(entry);
    if (!refs.length) {
        if (!entry.reason) return null;
        const note = document.createElement('p');
        note.className = 'usage-reason';
        note.textContent = entry.reason;
        return note;
    }

    const list = document.createElement('ul');
    list.className = 'usage-evidence';

    // One entry per dashboard, with its panels nested underneath.
    groupRefsByDashboard(refs).forEach((group) => {
        const ref = group.dashboard;
        const item = document.createElement('li');

        const link = document.createElement(ref.dashboard_url ? 'a' : 'span');
        link.className = 'usage-dashboard';
        link.textContent = ref.dashboard_title || ref.dashboard_uid || 'dashboard';
        if (ref.dashboard_url) {
            const url = panelURL(ref);
            if (url) {
                link.setAttribute('href', url);
                link.setAttribute('target', '_blank');
                link.setAttribute('rel', 'noopener noreferrer');
            }
        }
        item.appendChild(link);

        const named = group.panels.filter((panel) => panel.panel_title);
        if (named.length > 1) {
            const count = document.createElement('span');
            count.className = 'usage-panel';
            count.textContent = ` — ${named.length} panels`;
            item.appendChild(count);
        }

        group.via.forEach((via) => {
            const tag = document.createElement('span');
            tag.className = 'usage-via';
            tag.textContent = via;
            tag.title = 'Reached through ClickHouse lineage, not read directly.';
            item.appendChild(tag);
        });
        if (group.heuristic) {
            const guess = document.createElement('span');
            guess.className = 'usage-heuristic';
            guess.textContent = 'heuristic';
            guess.title = 'Attributed by matching names, not by parsing. Cannot license an unused verdict.';
            item.appendChild(guess);
        }

        const panels = document.createElement('ul');
        panels.className = 'usage-panels';
        named.slice(0, 6).forEach((panel) => {
            const row = document.createElement('li');
            const title = document.createElement('span');
            title.className = 'usage-panel';
            title.textContent = panel.panel_title;
            row.appendChild(title);
            if (panel.snippet) {
                const snippet = document.createElement('code');
                snippet.className = 'usage-snippet';
                snippet.textContent = panel.snippet;
                row.appendChild(snippet);
            }
            panels.appendChild(row);
        });
        if (named.length > 6) {
            const more = document.createElement('li');
            more.className = 'usage-panel';
            more.textContent = `… and ${named.length - 6} more panels`;
            panels.appendChild(more);
        }
        if (panels.childElementCount) item.appendChild(panels);

        list.appendChild(item);
    });

    return list;
}

// panelURL deep-links to the panel, refusing anything that is not an http(s)
// URL so a hostile dashboard record cannot inject a javascript: href.
function panelURL(ref) {
    // Directory mode has no base URL to build one from, and an empty string
    // resolves against window.location — a link back to this app.
    if (!ref.dashboard_url) return null;
    let url;
    try {
        url = new URL(ref.dashboard_url, window.location.origin);
    } catch (error) {
        return null;
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
    if (ref.panel_id) url.searchParams.set('viewPanel', String(ref.panel_id));
    return url.toString();
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
// Human-readable names for the exported page's subtitle and nothing else.
const SECTION_TITLES = {
    'data-flow': 'Data Flow',
    'relationships': 'Column Relationships',
    'dashboard-usage': 'Grafana Dashboard Usage',
};

function exportHtml() {
    if (!selectedDatabase || !selectedTable) {
        showError('No table selected.');
        return;
    }
    // Only the diagram sections have anything to export; the report is a table.
    const diagrams = {
        'data-flow': dataflowDiagram,
        'relationships': relationshipsDiagram,
        // Created only when Grafana is configured, hence the lookup rather than
        // a module-level const.
        'dashboard-usage': document.getElementById('dashboards-diagram'),
    };
    const sourceDiagram = diagrams[currentActiveSection];
    if (!sourceDiagram) {
        showError('Export HTML applies to the diagram views, not to the unused-columns report.');
        return;
    }
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
  --t-mergetree-fg:#1f6feb; --t-replicated-fg:#d97706; --t-distributed-fg:#0d9488; --t-mview-fg:#a855f7; --t-dictionary-fg:#b48a00;
  --grafana-fg:#f46800;
  --v-used-fg:#0f7b4f; --v-unused-fg:#b3261e; --v-unknown-fg:#8a6d0b; --v-nocoverage-fg:#9aa0aa; }
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
  <div class="crumb">schemaflow · ${escapeHtml(SECTION_TITLES[currentActiveSection] || currentActiveSection)}</div>
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
/* An SVG rect with no fill paints black. These are hit targets that are only
   ever visible on hover, so the live stylesheet makes them transparent —
   omitting them here turned every column row of an export into a black bar. */
.rel-col-hit, .du-dash-hit { fill:transparent; }
.rel-col-name { font: 400 11px 'JetBrains Mono', monospace; fill:var(--text); }
.rel-col-type { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--muted); }
.rel-edge { fill:none; stroke:var(--accent); stroke-opacity:.5; stroke-width:1.4; }
.rel-edge-label rect { fill:#fff; stroke:var(--border); }
.rel-edge-label text { font: 500 10px 'JetBrains Mono', monospace; fill:var(--accent); }
.du-card { fill:#fff; stroke:var(--border); stroke-width:1; }
.du-rail { fill:var(--grafana-fg); }
.du-dash-kind { font: 500 9.5px 'JetBrains Mono', monospace; fill:var(--grafana-fg); letter-spacing:.06em; }
.du-dash-count { font: 400 9.5px 'JetBrains Mono', monospace; fill:var(--muted); }
.du-dash-title { font: 700 12px 'JetBrains Mono', monospace; fill:var(--text); }
.du-dash-hit { fill:transparent; }
.du-panel-name { font: 400 11px 'JetBrains Mono', monospace; fill:var(--text); }
.du-panel-tag { font: 500 9px 'JetBrains Mono', monospace; fill:var(--muted); }
.du-panel-tag.derived { fill:var(--accent); }
.du-panel-tag.heuristic, .du-panel-tag.table-level { fill:var(--v-unknown-fg); }
.du-edge { fill:none; stroke:var(--grafana-fg); stroke-opacity:.55; stroke-width:1.4; }
.du-edge.derived { stroke-dasharray:5 3; }
.du-edge.heuristic { stroke-opacity:.3; stroke-dasharray:2 3; }
.du-edge.table-level { stroke:var(--v-unknown-fg); stroke-opacity:.45; stroke-dasharray:1 4; }
.du-more-text { font: italic 400 9.5px 'JetBrains Mono', monospace; fill:var(--faint); }
.du-col { --dot:var(--v-nocoverage-fg); }
.du-col.verdict-used { --dot:var(--v-used-fg); }
.du-col.verdict-unused { --dot:var(--v-unused-fg); }
.du-col.verdict-unknown { --dot:var(--v-unknown-fg); }
.du-dot { fill:none; stroke:var(--dot); stroke-width:1.6; }
.du-col.du-named .du-dot { fill:var(--dot); }
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
