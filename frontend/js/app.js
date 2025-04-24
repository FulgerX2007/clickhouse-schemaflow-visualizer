const databaseTree = document.getElementById('database-tree');
const refreshBtn = document.getElementById('refresh-btn');
const currentSelection = document.getElementById('current-selection');
const schemaDiagram = document.getElementById('schema-diagram');
const exportHtmlBtn = document.getElementById('export-html-btn');
const zoomInBtn = document.getElementById('zoom-in-btn');
const zoomOutBtn = document.getElementById('zoom-out-btn');
const resetZoomBtn = document.getElementById('reset-zoom-btn');

let databases = [];
let selectedDatabase = null;
let selectedTable = null;
let currentSchema = null;
let currentZoomLevel = 1; // Default zoom level

document.addEventListener('DOMContentLoaded', () => {
    mermaid.initialize({
        startOnLoad: false,
        theme: 'Neo',
        look: 'Neo',
        securityLevel: 'loose',
        flowchart: {
            useMaxWidth: true,
            htmlLabels: true
        }
    });
    console.log("Mermaid initialized successfully");

    loadDatabases();

    refreshBtn.addEventListener('click', loadDatabases);
    exportHtmlBtn.addEventListener('click', exportHtml);
    
    zoomInBtn.addEventListener('click', zoomIn);
    zoomOutBtn.addEventListener('click', zoomOut);
    resetZoomBtn.addEventListener('click', resetZoom);
    
});

async function loadDatabases() {
    try {
        const response = await fetch('/api/databases');
        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }

        databases = await response.json();
        console.log('Received databases structure:', databases);
        renderDatabaseTree();
    } catch (error) {
        console.error('Error loading databases:', error);
        showError('Failed to load databases. Please check your connection to ClickHouse.');
    }
}

function renderDatabaseTree() {
    databaseTree.innerHTML = '';

    if (typeof databases === 'object' && !Array.isArray(databases)) {
        Object.entries(databases).forEach(([dbName, dbContent]) => {
            const dbItem = document.createElement('li');

            const dbSpan = document.createElement('span');
            dbSpan.className = 'database';
            dbSpan.textContent = dbName;
            dbSpan.addEventListener('click', () => toggleDatabase(dbItem));

            dbItem.appendChild(dbSpan);

            const tablesList = document.createElement('ul');
            tablesList.style.display = 'none';

            if (typeof dbContent === 'object' && !Array.isArray(dbContent) && !dbContent.tables) {
                Object.keys(dbContent).forEach(tableName => {
                    addTableToList(tablesList, dbName, tableName);
                });
            } 
            else if (dbContent.tables && Array.isArray(dbContent.tables)) {
                dbContent.tables.forEach(table => {
                    const tableName = typeof table === 'string' ? table : table.name;
                    addTableToList(tablesList, dbName, tableName);
                });
            } else if (dbContent.tables && typeof dbContent.tables === 'object') {
                // If tables is an object, iterate through its keys
                Object.keys(dbContent.tables).forEach(tableName => {
                    addTableToList(tablesList, dbName, tableName);
                });
            } else {
                console.warn(`Handling structure for database ${dbName} as direct table list:`, dbContent);
                if (typeof dbContent === 'object') {
                    Object.keys(dbContent).forEach(key => {
                        addTableToList(tablesList, dbName, key);
                    });
                }
            }

            dbItem.appendChild(tablesList);
            databaseTree.appendChild(dbItem);
        });
    } else if (Array.isArray(databases)) {
        databases.forEach(db => {
            const dbItem = document.createElement('li');
            const dbSpan = document.createElement('span');
            dbSpan.className = 'database';
            dbSpan.textContent = db.name || db.toString();
            dbSpan.addEventListener('click', () => toggleDatabase(dbItem));
            dbItem.appendChild(dbSpan);

            const tablesList = document.createElement('ul');
            tablesList.style.display = 'none';

            if (db.tables) {
                if (Array.isArray(db.tables)) {
                    db.tables.forEach(table => {
                        const tableName = typeof table === 'string' ? table : table.name;
                        addTableToList(tablesList, db.name, tableName);
                    });
                } else if (typeof db.tables === 'object') {
                    Object.keys(db.tables).forEach(tableName => {
                        addTableToList(tablesList, db.name, tableName);
                    });
                }
            } else if (typeof db === 'object') {
                const dbName = db.name || db.toString();
                Object.keys(db)
                    .filter(key => key !== 'name')
                    .forEach(tableName => {
                        addTableToList(tablesList, dbName, tableName);
                    });
            }

            dbItem.appendChild(tablesList);
            databaseTree.appendChild(dbItem);
        });
    } else {
        console.error('Unexpected databases structure:', databases);
        showError('The database structure is not in the expected format.');
    }
}

function addTableToList(tablesList, dbName, tableName) {
    const tableItem = document.createElement('li');
    tableItem.className = 'table';
    tableItem.textContent = tableName;
    tableItem.dataset.database = dbName;
    tableItem.dataset.table = tableName;
    tableItem.title = tableName;

    tableItem.addEventListener('click', () => selectTable(tableItem));

    tablesList.appendChild(tableItem);
}

function toggleDatabase(dbItem) {
    const tablesList = dbItem.querySelector('ul');
    if (tablesList.style.display === 'none') {
        tablesList.style.display = 'block';
    } else {
        tablesList.style.display = 'none';
    }
}

async function selectTable(tableItem) {
    const previouslySelected = document.querySelector('.table.selected');
    if (previouslySelected) {
        previouslySelected.classList.remove('selected');
    }

    tableItem.classList.add('selected');

    selectedDatabase = tableItem.dataset.database;
    selectedTable = tableItem.dataset.table;

    currentSelection.textContent = `${selectedDatabase} / ${selectedTable}`;

    await loadTableSchema();
}

async function loadTableSchema() {
    if (!selectedDatabase || !selectedTable) return;

    try {
        const response = await fetch(`/api/schema/${selectedDatabase}/${selectedTable}`);
        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }

        const data = await response.json();
        currentSchema = data.schema;

        // Render the schema
        renderSchema();
    } catch (error) {
        console.error('Error loading table schema:', error);
        showError('Failed to load table schema.');
    }
}

function formatMermaidSchema(schema) {
    if (!schema || typeof schema !== 'string') return schema;

    console.log("Original schema:", schema);

    return schema;
}

function renderSchema() {
    if (!currentSchema) return;

    const formattedSchema = formatMermaidSchema(currentSchema);

    console.log("Formatted schema to render:", formattedSchema);

    if (typeof mermaid === 'undefined') {
        console.error("Mermaid is not defined when trying to render schema. Waiting for it to load...");
        showError("Diagram library is loading. Please wait a moment and try again.");

        const mermaidRenderInterval = setInterval(() => {
            if (typeof mermaid !== 'undefined') {
                clearInterval(mermaidRenderInterval);
                try {
                    console.log("Mermaid now available. Initializing with schema:", formattedSchema);
                    renderMermaidDiagram(formattedSchema);
                } catch (error) {
                    console.error("Error during Mermaid initialization after waiting:", error);
                    showError("Failed to render diagram. Check console for details.");
                }
            }
        }, 100);
        return;
    }

    try {
        renderMermaidDiagram(formattedSchema);
    } catch (error) {
        console.error("Error during Mermaid initialization:", error);
        showError("Failed to render diagram. Check console for details.");
    }
}

function renderMermaidDiagram(schema) {
    schemaDiagram.innerHTML = '';

    const container = document.createElement('div');
    container.className = 'mermaid';
    container.textContent = schema;
    schemaDiagram.appendChild(container);

    console.log("Rendering Mermaid diagram with schema:", schema);

    try {
        mermaid.initialize({
            startOnLoad: false,
            theme: 'default',
            securityLevel: 'loose',
            flowchart: {
                useMaxWidth: true,
                htmlLabels: true
            },
            er: {
                diagramPadding: 20,
                layoutDirection: 'TB',
                minEntityWidth: 100,
                minEntityHeight: 75,
                entityPadding: 15
            }
        });

        mermaid.init(undefined, '.mermaid');
        console.log("Mermaid initialization successful");
        
        applyZoom();
        
        setupMouseWheelZoom();
    } catch (error) {
        console.error("Error during Mermaid initialization:", error);
        // Fallback to show raw schema
        showRawSchema(schema);
    }
}

function showRawSchema(schema) {
    schemaDiagram.innerHTML = '';
    const rawSchemaDisplay = document.createElement('pre');
    rawSchemaDisplay.style.whiteSpace = 'pre-wrap';
    rawSchemaDisplay.style.fontFamily = 'monospace';
    rawSchemaDisplay.style.padding = '10px';
    rawSchemaDisplay.style.border = '1px solid #ccc';
    rawSchemaDisplay.textContent = schema;
    schemaDiagram.appendChild(rawSchemaDisplay);
    showError("Failed to render diagram. Showing raw schema instead.");
}

function exportHtml() {
    if (!currentSchema) {
        showError('No schema to export.');
        return;
    }

    const exportSchema = formatMermaidSchema(currentSchema);

    const html = `
        <!DOCTYPE html>
        <html lang="en">
        <head>
            <meta charset="UTF-8">
            <meta name="viewport" content="width=device-width, initial-scale=1.0">
            <title>${selectedDatabase} - ${selectedTable} Schema</title>
            <script src="https://cdn.jsdelivr.net/npm/mermaid@11.6.0/dist/mermaid.min.js" crossorigin="anonymous" defer></script>
            <style>
                body { font-family: Arial, sans-serif; margin: 20px; }
                h1 { color: #2c3e50; }
                .mermaid { font-family: 'Courier New', Courier, monospace; }
                .raw-schema { 
                    white-space: pre-wrap; 
                    font-family: monospace; 
                    padding: 10px; 
                    border: 1px solid #ccc; 
                    margin-top: 20px;
                    display: none;
                }
            </style>
        </head>
        <body>
            <h1>${selectedDatabase} - ${selectedTable} Schema</h1>
            <pre class="mermaid">
${exportSchema}
            </pre>
            <div id="raw-schema" class="raw-schema">
${exportSchema}
            </div>
            <script>
                document.addEventListener('DOMContentLoaded', function() {
                    const rawSchema = document.getElementById('raw-schema');

                    function showRawSchema() {
                        rawSchema.style.display = 'block';
                    }

                    if (typeof mermaid !== 'undefined') {
                        try {
                            console.log("Initializing Mermaid in exported HTML");

                            mermaid.initialize({
                                startOnLoad: false,
                                theme: 'default',
                                securityLevel: 'loose',
                                flowchart: {
                                    useMaxWidth: true,
                                    htmlLabels: true
                                },
                                er: {
                                    diagramPadding: 20,
                                    layoutDirection: 'TB',
                                    minEntityWidth: 100,
                                    minEntityHeight: 75,
                                    entityPadding: 15
                                }
                            });

                            try {
                                mermaid.init(undefined, '.mermaid');
                                console.log("Mermaid initialization successful");
                            } catch (renderError) {
                                console.error("Mermaid render error:", renderError);
                                showRawSchema();
                            }
                        } catch (error) {
                            console.error("Error during Mermaid initialization:", error);
                            showRawSchema();
                        }
                    } else {
                        console.error("Mermaid is not defined in exported HTML");
                        showRawSchema();

                        const mermaidCheckInterval = setInterval(function() {
                            if (typeof mermaid !== 'undefined') {
                                clearInterval(mermaidCheckInterval);
                                try {
                                    console.log("Mermaid now available in exported HTML");

                                    mermaid.initialize({
                                        startOnLoad: false,
                                        theme: 'default',
                                        securityLevel: 'loose',
                                        flowchart: {
                                            useMaxWidth: true,
                                            htmlLabels: true
                                        },
                                        er: {
                                            diagramPadding: 20,
                                            layoutDirection: 'TB',
                                            minEntityWidth: 100,
                                            minEntityHeight: 75,
                                            entityPadding: 15
                                        }
                                    });

                                    try {
                                        mermaid.init(undefined, '.mermaid');
                                        console.log("Mermaid initialization successful after waiting");
                                        rawSchema.style.display = 'none';
                                    } catch (renderError) {
                                        console.error("Mermaid render error after waiting:", renderError);
                                        showRawSchema();
                                    }
                                } catch (error) {
                                    console.error("Error during Mermaid initialization after waiting:", error);
                                    showRawSchema();
                                }
                            }
                        }, 100);
                    }
                });
            </script>
        </body>
        </html>
    `;

    // Create a blob and download link
    const blob = new Blob([html], { type: 'text/html' });
    const url = URL.createObjectURL(blob);

    const a = document.createElement('a');
    a.href = url;
    a.download = `${selectedDatabase}_${selectedTable}_schema.html`;
    document.body.appendChild(a);
    a.click();

    // Clean up
    setTimeout(() => {
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }, 100);
}

function zoomIn() {
    currentZoomLevel = Math.min(currentZoomLevel + 0.1, 3);
    applyZoom();
}

function zoomOut() {
    currentZoomLevel = Math.max(currentZoomLevel - 0.1, 0.5);
    applyZoom();
}

function resetZoom() {
    currentZoomLevel = 1;
    applyZoom();
}

function applyZoom() {
    if (schemaDiagram) {
        schemaDiagram.style.transform = `scale(${currentZoomLevel})`;
        
        const schemaContainer = document.querySelector('.schema-container');
        if (schemaContainer) {
            if (currentZoomLevel > 1) {
                schemaContainer.style.overflow = 'auto';
            } else {
                schemaContainer.style.overflow = 'auto';
            }
        }
    }
}

function setupMouseWheelZoom() {
    const schemaContainer = document.querySelector('.schema-container');
    if (!schemaContainer) return;
    
    schemaContainer.removeEventListener('wheel', handleMouseWheel);
    schemaContainer.addEventListener('wheel', handleMouseWheel, { passive: false });
    
    console.log("Mouse wheel zoom support set up");
}

function handleMouseWheel(event) {
    event.preventDefault();
    
    const delta = event.deltaY || event.detail || event.wheelDelta;
    
    if (delta < 0) {
        zoomIn();
    } else {
        zoomOut();
    }
    
    console.log(`Zoom level: ${currentZoomLevel.toFixed(1)}`);
}

function showError(message) {
    alert(message);
}
