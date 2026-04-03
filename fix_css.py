new_rules = """
/* ==========================================================================
   CONSOLIDATED & REFINED WEBSITE RESPONSIVENESS
   ========================================================================== */

#website-section {
    container-type: inline-size;
    container-name: website-section;
}

@container website-section (max-width: 1280px) {
    .website-toolbar {
        display: grid;
        grid-template-columns: 1fr;
        gap: 16px;
    }

    .website-toolbar-left {
        justify-content: flex-start;
    }

    .website-toolbar-copy {
        justify-items: start;
        text-align: left;
    }

    .website-table-head,
    .website-row {
        grid-template-columns: 34px minmax(200px, 1.8fr) 90px minmax(110px, 0.9fr) minmax(110px, 0.9fr) minmax(80px, 0.7fr);
    }

    .website-table-head span:nth-child(6),
    .website-table-head span:nth-child(7),
    .website-table-head span:nth-child(8) {
        display: none !important;
    }

    .website-row > :is(.website-ssl, .website-requests, .website-waf) {
        display: none !important;
    }
}

@container website-section (max-width: 960px) {
    .website-table-head {
        display: none !important;
    }

    .website-row {
        display: grid !important;
        grid-template-columns: 1fr 1fr !important;
        grid-template-rows: auto auto auto !important;
        gap: 12px !important;
        position: relative !important;
        padding: 24px 20px !important;
        border-bottom: 2px solid var(--line-soft) !important;
        background: #fff !important;
    }

    .website-check {
        position: absolute !important;
        top: 24px !important;
        right: 20px !important;
        z-index: 5 !important;
        width: auto !important;
    }

    /* Header: Name and Status together */
    .website-site {
        grid-column: 1 / -1 !important;
        display: flex !important;
        flex-wrap: wrap !important;
        align-items: center !important;
        gap: 10px 16px !important;
        margin-bottom: 4px !important;
        padding-right: 48px !important;
    }

    .website-domain-link {
        font-size: 1.15rem !important;
    }

    .website-status-cell {
        display: flex !important;
        align-items: center !important;
        gap: 8px !important;
        background: #f1f5f9;
        padding: 5px 12px;
        border-radius: 999px;
        width: fit-content;
        margin-left: 0 !important;
    }

    .website-status-text {
        display: inline !important;
        font-size: 0.75rem !important;
        font-weight: 800 !important;
        color: #64748b !important;
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .website-status {
        width: 14px !important;
        height: 14px !important;
    }

    /* Properties Grid */
    .website-php-version,
    .website-expiration,
    .website-ssl,
    .website-requests,
    .website-waf,
    .website-operate {
        display: block !important;
        background: #f8fafc !important;
        padding: 14px 16px !important;
        border-radius: 12px !important;
        border: 1px solid #f1f5f9 !important;
        min-width: 0 !important;
    }

    .website-php-version::before,
    .website-expiration::before,
    .website-ssl::before,
    .website-requests::before,
    .website-waf::before,
    .website-operate::before {
        content: attr(data-label) !important;
        display: block !important;
        font-size: 0.65rem !important;
        font-weight: 800 !important;
        color: #7b93aa !important;
        text-transform: uppercase !important;
        letter-spacing: 0.1em !important;
        margin-bottom: 6px !important;
    }

    .website-requests {
        display: flex !important;
        flex-direction: column !important;
        align-items: flex-start !important;
        gap: 6px !important;
    }

    .website-requests-value {
        display: inline !important;
    }

    .website-actions {
        width: 100% !important;
        display: grid !important;
        grid-template-columns: 1fr !important;
    }

    .website-actions button {
        width: 100% !important;
        padding: 8px !important;
        background: #f1f5f9 !important;
        border-radius: 8px !important;
        text-align: center !important;
        color: var(--text-main) !important;
        font-size: 0.9rem !important;
    }
}

@container website-section (max-width: 540px) {
    .website-row {
        grid-template-columns: 1fr !important;
    }

    .website-toolbar-left {
        display: grid !important;
        grid-template-columns: repeat(2, 1fr) !important;
        width: 100% !important;
    }
}

@container website-section (max-width: 400px) {
    .website-toolbar-left {
        grid-template-columns: 1fr !important;
    }
}
"""

with open('static/styles.css', 'r', encoding='utf-8') as f:
    lines = f.readlines()

# Find the start of the outdated blocks to remove
start_line = -1
for i, line in enumerate(lines):
    if '@container website-section (max-width: 920px)' in line and i > 4000:
        start_line = i
        break
    if '/* Consolidated Website Section Responsiveness */' in line:
        start_line = i
        break
    if 'CONSOLIDATED MOBILE FIXES' in line:
        start_line = i
        break

if start_line != -1:
    print(f"Applying final cleanup from line {start_line+1}")
    new_content = lines[:start_line] + [new_rules]
    with open('static/styles.css', 'w', encoding='utf-8') as f:
        f.writelines(new_content)
else:
    print("Anchor not found, appending to end.")
    with open('static/styles.css', 'a', encoding='utf-8') as f:
        f.write(new_rules)
