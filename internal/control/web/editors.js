(function () {
  'use strict';
  function create(env) {
    const {api,save,publishConfig,getConfig,getSession,getObservedRevision,t,esc,errorText,notice,withBusy,openModal} = env;
    const $ = id => document.getElementById(id);
    const et = (text,values) => esc(t(text,values));
    const lines = value => value.split(/[\n,]/).map(value => value.trim()).filter(Boolean);
    const collections = new Set(['rules','outbound_groups','rule_sets']);
    let epoch = 0;
    let bound = false, groupDraft = null, setDraft = null, opening = false, members = [], deletingGroup = null, deletingSet = null;
    let previewSequence = 0, preview = null, observedRevision = null;
    const sameSession = generation => getSession().active && getSession().generation === generation;
    function failure(source) { const error = new Error(t(source)); error.source = source; return error; }
    function assertSession(draft) { if(!sameSession(draft.generation) || draft.epoch !== epoch) { const error = new Error(); error.stale = true; throw error; } }
    function createDraft(collection,originalID = null) {
      if(!collections.has(collection)) throw new TypeError('Unsupported draft collection');
      const baseConfig = structuredClone(getConfig());
      const baseEntity = originalID === null ? null : baseConfig[collection]?.find(item => item.id === originalID);
      if(originalID !== null && !baseEntity) throw failure('This item was deleted elsewhere. Close this editor to create a new item.');
      return {collection,originalID,baseConfig,baseRevision:baseConfig.revision,baseEntity:structuredClone(baseEntity),dirty:false,saving:false,reviewBase:null,requiresReview:false,deleted:false,generation:getSession().generation,epoch};
    }
    async function reviewDraft(draft) {
      assertSession(draft);
      draft.reviewBase = null; draft.requiresReview = true;
      const latest = await api('/config');
      assertSession(draft);
      const entity = draft.originalID === null ? null : latest[draft.collection]?.find(item => item.id === draft.originalID);
      if(draft.originalID !== null && !entity) {
        draft.deleted = true; draft.reviewBase = null;
        throw failure('This item was deleted elsewhere. Close this editor to create a new item.');
      }
      draft.reviewBase = structuredClone(latest); draft.requiresReview = false;
      return structuredClone(entity);
    }
    async function saveDraft(draft,entity) {
      assertSession(draft);
      if(draft.saving) return false;
      if(draft.deleted) throw failure('This item was deleted elsewhere. Close this editor to create a new item.');
      if(draft.requiresReview && !draft.reviewBase) throw failure('Review latest settings before saving your draft.');
      const next = structuredClone(draft.reviewBase || draft.baseConfig);
      const list = next[draft.collection] || [];
      const index = draft.originalID === null ? -1 : list.findIndex(item => item.id === draft.originalID);
      if(draft.originalID !== null && index < 0) {
        draft.deleted = true;
        throw failure('This item was deleted elsewhere. Close this editor to create a new item.');
      }
      if(list.some((item,i) => item.id === entity.id && i !== index)) throw failure('This ID is already in use. Choose another ID.');
      if(index >= 0) list[index] = structuredClone(entity);
      else if(draft.collection === 'rules') list.unshift(structuredClone(entity));
      else list.push(structuredClone(entity));
      next[draft.collection] = list; draft.saving = true;
      try {
        await save(next); assertSession(draft); draft.dirty = false; return true;
      } catch(error) {
        if(!sameSession(draft.generation)) { error.stale = true; throw error; }
        if(error.status === 409) { draft.reviewBase = null; draft.requiresReview = true; }
        throw error;
      } finally { draft.saving = false; }
    }
    const groupTypeName = value => { const source = {select:'Select',fallback:'Fallback','load-balance':'Load balance'}[value]; return source ? t(source) : value; };
    function setError(prefix,error) {
      const node = $(prefix === 'group' ? 'group-error' : 'rs-error');
      node.textContent = errorText(error); node.dataset.errorSource = error.source || '';
    }
    function renderConflict(prefix,draft) {
      const reviewing = !!draft?.reviewBase;
      if(!reviewing) $(`${prefix}-review-panel`).hidden = true;
      $(`${prefix}-conflict`).hidden = !draft?.requiresReview && !reviewing && !draft?.deleted;
      $(`${prefix}-conflict-message`).textContent = t(draft?.deleted ? 'This item was deleted elsewhere. Close this editor to create a new item.' : 'Configuration changed elsewhere. Your draft is kept.');
      $(`${prefix}-review`).hidden = !draft?.requiresReview || !!draft?.deleted;
      $(`${prefix}-save`).disabled = !!draft?.saving || !!draft?.deleted || (!!draft?.requiresReview && !reviewing);
      $(`${prefix}-save`).textContent = t(reviewing ? 'Save my draft' : prefix === 'group' ? 'Save group' : 'Save rule set');
    }
    function renderGroups() {
      const groups = getConfig()?.outbound_groups || [];
      $('group-list').innerHTML = groups.map(group => `<article class="outbound-card group-card"><div class="outbound-heading"><h3>${esc(group.id)}</h3><span class="badge">${esc(groupTypeName(group.type))}</span><span class="badge">${et(group.enabled ? 'Enabled' : 'Disabled')}</span></div><p class="group-members-summary">${et('Members in configured order')}: ${esc(group.members.join(' → '))}</p>${group.type === 'select' ? `<p>${et('Selected member')}: ${esc(group.selected)}</p>` : ''}<div class="button-row"><button type="button" class="quiet" data-edit-group="${esc(group.id)}">${et('Edit')}</button><button type="button" class="quiet danger" data-delete-group="${esc(group.id)}">${et('Delete')}</button></div></article>`).join('');
      $('groups-empty').hidden = groups.length > 0;
    }
    function renderRuleSets() {
      const config = getConfig(), sets = config?.rule_sets || [];
      $('rule-set-list').innerHTML = sets.map(set => {
        const conditions = ['domains','suffixes','cidrs','sources'].flatMap(key => set[key] || []);
        const references = (config.rules || []).filter(rule => (rule.sets || []).includes(set.id)).map(rule => rule.id);
        return `<tr><td data-label="${et('Name / ID')}"><strong>${esc(set.id)}</strong><span class="sub">${esc(set.name)}</span></td><td data-label="${et('Conditions')}">${esc(conditions.join(', '))}</td><td data-label="${et('Referenced by rules')}">${esc(references.join(', ') || t('None'))}</td><td data-label="${et('Actions')}"><button type="button" class="table-action" data-edit-set="${esc(set.id)}">${et('Edit')}</button><button type="button" class="table-action danger" data-delete-set="${esc(set.id)}">${et('Delete')}</button></td></tr>`;
      }).join('');
      $('rule-sets-empty').hidden = sets.length > 0;
    }
    function renderMembers() {
      const focused = document.activeElement, focusedID = focused?.closest('[data-member]')?.dataset.member;
      const focusedAction = focused?.matches('[data-member-up]') ? '[data-member-up]' : focused?.matches('[data-member-down]') ? '[data-member-down]' : 'input';
      const eligible = (groupDraft?.baseConfig.outbounds || []).filter(outbound => outbound.enabled && outbound.type !== 'direct');
      const ordered = [...members,...eligible.map(item => item.id).filter(id => !members.includes(id))];
      $('group-members').innerHTML = ordered.map(id => {
        const index = members.indexOf(id), selected = index >= 0, available = eligible.some(item => item.id === id);
        return `<div class="group-member-row" data-member="${esc(id)}"><label class="check"><input type="checkbox" value="${esc(id)}" data-group-member${selected ? ' checked' : ''}${available ? '' : ' disabled'}>${esc(id)}${available ? '' : ` · ${et('Unavailable')}`}</label><div class="group-member-controls"><button type="button" class="quiet" data-member-up="${esc(id)}" aria-label="${et('Move {name} up',{name:id})}"${index <= 0 ? ' disabled' : ''}>↑</button><button type="button" class="quiet" data-member-down="${esc(id)}" aria-label="${et('Move {name} down',{name:id})}"${!selected || index === members.length-1 ? ' disabled' : ''}>↓</button></div></div>`;
      }).join('') || `<p class="hint">${et('Enable a non-direct outbound before adding a group.')}</p>`;
      const selected = $('group-selected').value;
      $('group-selected').innerHTML = `<option value="">${et('Choose a member')}</option>` + members.map(id => `<option value="${esc(id)}">${esc(id)}</option>`).join('');
      $('group-selected').value = members.includes(selected) ? selected : '';
      if(focusedID) [...$('group-members').querySelectorAll('[data-member]')].find(row => row.dataset.member === focusedID)?.querySelector(focusedAction)?.focus({preventScroll:true});
    }
    function changeGroupType() {
      const type = $('group-type').value, strategy = $('group-strategy').value;
      $('group-strategy-section').hidden = type !== 'load-balance';
      $('group-key-section').hidden = type !== 'load-balance' || strategy !== 'consistent-hashing';
      $('group-selected-section').hidden = type !== 'select';
      if(type !== 'select') $('group-selected').value = '';
      if(type !== 'load-balance') { $('group-strategy').value = ''; $('group-key').value = ''; }
      else if(strategy !== 'consistent-hashing') $('group-key').value = '';
      $('group-strategy').required = type === 'load-balance';
      $('group-key').required = type === 'load-balance' && strategy === 'consistent-hashing';
      $('group-selected').required = type === 'select';
    }
    function fillGroup(group) {
      $('group-id').value = group.id; $('group-id').disabled = groupDraft.originalID !== null;
      $('group-type').value = group.type; $('group-enabled').checked = group.enabled;
      members = [...group.members]; $('group-strategy').value = group.strategy || ''; $('group-key').value = group.key || '';
      renderMembers(); $('group-selected').value = group.selected || ''; changeGroupType();
    }
    function groupEntity() {
      const group = {id:$('group-id').value.trim(),type:$('group-type').value,enabled:$('group-enabled').checked,members:[...members]};
      if(group.type === 'select') group.selected = $('group-selected').value;
      if(group.type === 'load-balance') { group.strategy = $('group-strategy').value; if(group.strategy === 'consistent-hashing') group.key = $('group-key').value; }
      return group;
    }
    async function openGroup(id = null,trigger = document.activeElement) {
      if(opening || $('group-dialog').open) return;
      opening = true; const generation = getSession().generation;
      try {
        const defaults = id === null ? await api('/defaults') : null;
        if(!sameSession(generation)) return;
        groupDraft = createDraft('outbound_groups',id);
        fillGroup(id === null ? defaults.group : groupDraft.baseEntity);
        $('group-error').textContent = ''; delete $('group-error').dataset.errorSource; $('group-review-panel').hidden = true; renderConflict('group',groupDraft);
        openModal($('group-dialog'),trigger,$('add-group'));
      } catch(error) { if(sameSession(generation)) notice(error,true); }
      finally { if(sameSession(generation)) opening = false; }
    }
    function setEntity() { return {id:$('rs-id').value.trim(),name:$('rs-name').value.trim(),...Object.fromEntries(['domains','suffixes','cidrs','sources'].map(key => [key,lines($(`rs-${key}`).value)]))}; }
    async function openRuleSet(id = null,trigger = document.activeElement) {
      if(opening || $('rule-set-dialog').open) return;
      opening = true; const generation = getSession().generation;
      try {
        const defaults = id === null ? await api('/defaults') : null;
        if(!sameSession(generation)) return;
        setDraft = createDraft('rule_sets',id); const set = id === null ? defaults.rule_set : setDraft.baseEntity;
        $('rs-id').value = set.id; $('rs-id').disabled = id !== null; $('rs-name').value = set.name;
        for(const key of ['domains','suffixes','cidrs','sources']) $(`rs-${key}`).value = (set[key] || []).join('\n');
        $('rs-error').textContent = ''; delete $('rs-error').dataset.errorSource; $('rs-review-panel').hidden = true; renderConflict('rs',setDraft);
        openModal($('rule-set-dialog'),trigger,$('add-rule-set'));
      } catch(error) { if(sameSession(generation)) notice(error,true); }
      finally { if(sameSession(generation)) opening = false; }
    }
    function requestClose(dialog,draft) {
      if(draft?.saving) return;
      if(draft?.dirty && !confirm(t('Discard unsaved changes?'))) return;
      dialog.close();
    }
    async function reviewEditor(prefix,draft,entity) {
      if(!draft) return;
      const generation = draft.generation;
      const current = () => sameSession(generation) && draft === (prefix === 'group' ? groupDraft : setDraft);
      try {
        const latest = await reviewDraft(draft);
        if(!current()) return;
        $(`${prefix}-latest`).textContent = JSON.stringify(latest,null,2);
        $(`${prefix}-mine`).textContent = JSON.stringify(entity(),null,2);
        $(`${prefix}-review-panel`).hidden = false;
      } catch(error) { if(current()) setError(prefix,error); }
      if(current()) renderConflict(prefix,draft);
    }
    function moveMember(id,destination,action) {
      if(!groupDraft || groupDraft.saving) return;
      const index = members.indexOf(id); if(index < 0) return;
      const next = Math.max(0,Math.min(members.length-1,destination)); if(index === next) return;
      members.splice(index,1); members.splice(next,0,id); groupDraft.dirty = true; renderMembers();
      const row = [...$('group-members').querySelectorAll('[data-member]')].find(row => row.dataset.member === id);
      const button = row?.querySelector(action);
      (button && !button.disabled ? button : row?.querySelector('button:not(:disabled)') || row?.querySelector('input'))?.focus();
    }
    function renderDeleteGroup() {
      if(!deletingGroup) return;
      const {config,id} = deletingGroup;
      const references = (config.rules || []).filter(rule => rule.outbound === id).map(rule => rule.id);
      if(config.default_outbound === id) references.unshift(t('Default outbound'));
      $('group-delete-name').textContent = id;
      $('group-delete-references').innerHTML = references.map(reference => `<li>${esc(reference)}</li>`).join('');
      $('group-delete-replacement-section').hidden = references.length === 0;
      $('group-delete-replacement').required = references.length > 0;
      const selected = $('group-delete-replacement').value;
      const targets = [...config.outbounds,...(config.outbound_groups || [])].filter(target => target.enabled && target.id !== id);
      $('group-delete-replacement').innerHTML = `<option value="">${et('Choose a replacement')}</option>` + targets.map(target => `<option value="${esc(target.id)}">${esc(target.id)}</option>`).join('');
      $('group-delete-replacement').value = selected;
    }
    function openDeleteGroup(id,trigger) {
      if($('group-delete-dialog').open) return;
      const config = structuredClone(getConfig()); if(!config.outbound_groups?.some(group => group.id === id)) return;
      deletingGroup = {id,config,generation:getSession().generation};
      $('group-delete-error').textContent = ''; delete $('group-delete-error').dataset.errorSource; $('group-delete-reload').hidden = true; renderDeleteGroup(); openModal($('group-delete-dialog'),trigger,$('add-group'));
    }
    function openDeleteSet(id,trigger) {
      if($('rule-set-delete-dialog').open) return;
      const config = structuredClone(getConfig()); if(!config.rule_sets?.some(set => set.id === id)) return;
      const references = config.rules.filter(rule => (rule.sets || []).includes(id)).map(rule => rule.id);
      deletingSet = {id,config,generation:getSession().generation};
      $('rs-delete-reload').hidden = true;
      $('rs-delete-name').textContent = id;
      $('rs-delete-references').textContent = references.join(', ');
      delete $('rs-delete-error').dataset.errorSource;
      $('rs-delete-error').textContent = references.length ? t('Remove this rule set from its routing rules before deleting it.') : '';
      $('rs-delete-confirm').disabled = references.length > 0;
      openModal($('rule-set-delete-dialog'),trigger,$('add-rule-set'));
    }
    const reasonName = value => { const source = {default:'Default route',fixed_rule:'Fixed rule',adaptive_initial:'Initial adaptive route',group_select:'Selected group member',group_fallback:'Fallback member order',group_consistent_hashing:'Consistent hashing order',group_round_robin:'Round-robin order',private_protected:'Private destination protected',target_unavailable:'Target unavailable',no_eligible_member:'No eligible member',selected_unavailable:'Selected member unavailable'}[value]; return source ? t(source) : value; };
    const kindName = value => { const source = {outbound:'Outbound',group:'Outbound group',default:'Default route'}[value]; return source ? t(source) : value; };
    function previewRequest() { return {host:$('rt-host').value.trim(),port:$('rt-port').value.trim(),source:$('rt-source').value.trim(),inbound:$('rt-inbound').value,family:$('rt-family').value}; }
    function renderPreview() {
      const result = $('rt-result'); if(!preview) { result.textContent = ''; return; }
      const query = `<p class="route-preview-query">${et('Preview query')}: ${esc(JSON.stringify(preview.query))}</p>`;
      if(preview.stale) { result.innerHTML = query + `<p class="form-alert warn">${et('Inputs or configuration changed. Run the preview again.')}</p>`; return; }
      if(preview.error) { result.innerHTML = query; const error = document.createElement('p'); error.className = 'form-error error'; error.textContent = errorText(preview.error); result.append(error); return; }
      if(!preview.result) { result.innerHTML = query + `<p>${et('Loading…')}</p>`; return; }
      const value = preview.result;
      const fields = [['Matched rule',value.rule_id || t('None')],['Target kind',kindName(value.target_kind)],['Target ID',value.target_id],['Resolved outbound',value.resolved_id || t('Not resolved')],['Attempt order',(value.order || []).join(' → ') || t('None')],['Private destination',t(value.private ? 'Yes' : 'No')],['IP family',value.family],['Reason',reasonName(value.reason)]];
      result.innerHTML = query + (value.blocked ? `<p class="form-alert warn">${et('This route would be blocked.')}</p>` : '') + `<dl class="route-preview-grid">${fields.map(([name,text]) => `<dt>${et(name)}</dt><dd>${esc(text)}</dd>`).join('')}</dl>`;
    }
    function invalidatePreview() {
      previewSequence++; if(preview) preview.stale = true;
      $('rt-result')?.removeAttribute('aria-busy'); renderPreview();
    }
    async function runPreview() {
      const query = previewRequest(), signature = JSON.stringify(query), sequence = ++previewSequence;
      const generation = getSession().generation, revision = getObservedRevision();
      preview = {query}; renderPreview(); $('rt-result').setAttribute('aria-busy','true');
      const current = () => sequence === previewSequence && sameSession(generation) && revision === getObservedRevision() && signature === JSON.stringify(previewRequest());
      try {
        const result = await api('/route/explain',{method:'POST',body:JSON.stringify(query)});
        if(!current()) return;
        preview = {query,result}; renderPreview();
      } catch(error) { if(current()) { preview = {query,error}; renderPreview(); } }
      finally { if(sequence === previewSequence) $('rt-result').removeAttribute('aria-busy'); }
    }
    function configChanged() {
      const revision = getObservedRevision();
      if(observedRevision !== revision) { observedRevision = revision; invalidatePreview(); }
    }
    function render() {
      if(!getConfig()) return;
      renderGroups(); renderRuleSets(); renderDeleteGroup(); renderPreview();
      if(groupDraft) { renderConflict('group',groupDraft); renderMembers(); }
      if(setDraft) renderConflict('rs',setDraft);
      for(const prefix of ['group','rs']) { const error = $(`${prefix}-error`); if(error?.dataset.errorSource) error.textContent = t(error.dataset.errorSource); }
    }
    function reset() {
      epoch++; previewSequence++; preview = null; observedRevision = null; groupDraft = null; setDraft = null; members = []; deletingGroup = null; deletingSet = null; opening = false;
      for(const id of ['group-dialog','group-delete-dialog','rule-set-dialog','rule-set-delete-dialog']) $(id)?.close();
      for(const id of ['group-form','rule-set-form']) $(id)?.reset?.();
      for(const prefix of ['group','rs']) {
        $(`${prefix}-latest`).textContent = ''; $(`${prefix}-mine`).textContent = '';
        $(`${prefix}-review-panel`).hidden = true; $(`${prefix}-conflict`).hidden = true;
        $(`${prefix}-error`).textContent = ''; delete $(`${prefix}-error`).dataset.errorSource;
      }
      $('rt-result')?.removeAttribute('aria-busy'); if($('rt-result')) $('rt-result').textContent = '';
      $('rt-form')?.reset?.();
    }
    function bind() {
      if(bound) return; bound = true;
      $('add-group').addEventListener('click',event => openGroup(null,event.currentTarget));
      $('add-rule-set').addEventListener('click',event => openRuleSet(null,event.currentTarget));
      $('group-list').addEventListener('click',event => { const edit = event.target.closest('[data-edit-group]'), del = event.target.closest('[data-delete-group]'); if(edit) openGroup(edit.dataset.editGroup,edit); if(del) openDeleteGroup(del.dataset.deleteGroup,del); });
      $('rule-set-list').addEventListener('click',event => { const edit = event.target.closest('[data-edit-set]'), del = event.target.closest('[data-delete-set]'); if(edit) openRuleSet(edit.dataset.editSet,edit); if(del) openDeleteSet(del.dataset.deleteSet,del); });
      for(const [prefix,dialogID,formID,getDraft,entity] of [['group','group-dialog','group-form',() => groupDraft,groupEntity],['rs','rule-set-dialog','rule-set-form',() => setDraft,setEntity]]) {
        const dialog = $(dialogID), form = $(formID);
        form.addEventListener('input',() => { const draft = getDraft(); if(draft) draft.dirty = true; });
        form.addEventListener('change',() => { const draft = getDraft(); if(draft) draft.dirty = true; });
        dialog.addEventListener('cancel',event => { event.preventDefault(); requestClose(dialog,getDraft()); });
        dialog.querySelectorAll('[data-editor-close]').forEach(button => button.addEventListener('click',() => requestClose(dialog,getDraft())));
        dialog.addEventListener('close',() => {
          if(dialog.open) return;
          if(prefix === 'group') groupDraft = null; else setDraft = null;
          form.reset(); $(`${prefix}-latest`).textContent = ''; $(`${prefix}-mine`).textContent = ''; $(`${prefix}-review-panel`).hidden = true;
        });
        $(`${prefix}-review`).addEventListener('click',() => withBusy($(`${prefix}-review`),() => reviewEditor(prefix,getDraft(),entity)));
        form.addEventListener('submit',event => {
          event.preventDefault(); const draft = getDraft(); if(!draft || draft.saving || $(`${prefix}-save`).disabled) return;
          withBusy($(`${prefix}-save`),async () => {
            try { if(await saveDraft(draft,entity())) dialog.close(); }
            catch(error) { if(!error.stale && sameSession(draft.generation)) setError(prefix,error); }
          }).finally(() => { if(getDraft() === draft) renderConflict(prefix,draft); });
        });
      }
      $('group-type').addEventListener('change',changeGroupType); $('group-strategy').addEventListener('change',changeGroupType);
      $('group-members').addEventListener('change',event => { if(!event.target.matches('[data-group-member]') || groupDraft?.saving) return; const id = event.target.value; if(event.target.checked) { if(!members.includes(id)) members.push(id); } else members = members.filter(member => member !== id); groupDraft.dirty = true; renderMembers(); });
      $('group-members').addEventListener('click',event => { const up = event.target.closest('[data-member-up]'), down = event.target.closest('[data-member-down]'); const id = up?.dataset.memberUp || down?.dataset.memberDown; if(id) moveMember(id,members.indexOf(id)+(up ? -1 : 1),up ? '[data-member-up]' : '[data-member-down]'); });
      $('group-members').addEventListener('keydown',event => { if(!event.altKey || !['ArrowUp','ArrowDown','Home','End'].includes(event.key)) return; const row = event.target.closest('[data-member]'); if(!row) return; event.preventDefault(); const index = members.indexOf(row.dataset.member); const destination = {ArrowUp:index-1,ArrowDown:index+1,Home:0,End:members.length-1}[event.key]; moveMember(row.dataset.member,destination,event.target.matches('input') ? 'input' : event.target.matches('[data-member-up]') ? '[data-member-up]' : '[data-member-down]'); });
      $('group-delete-dialog').addEventListener('close',() => { deletingGroup = null; });
      $('rule-set-delete-dialog').addEventListener('close',() => { deletingSet = null; });
      $('group-delete-form').addEventListener('submit',event => {
        event.preventDefault(); const context = deletingGroup; if(!context) return;
        withBusy($('group-delete-confirm'),async () => {
          try {
            const next = await api(`/groups/${encodeURIComponent(context.id)}`,{method:'DELETE',body:JSON.stringify({revision:context.config.revision,replacement:$('group-delete-replacement-section').hidden ? '' : $('group-delete-replacement').value})});
            if(!sameSession(context.generation)) return;
            publishConfig(next); if(deletingGroup === context) $('group-delete-dialog').close(); notice('Outbound group deleted.');
          } catch(error) { if(sameSession(context.generation) && deletingGroup === context) { $('group-delete-error').textContent = errorText(error); $('group-delete-error').dataset.errorSource = error.source || ''; if(error.status === 409) $('group-delete-reload').hidden = false; } }
        });
      });
      $('rs-delete-form').addEventListener('submit',event => {
        event.preventDefault(); const context = deletingSet; if(!context || $('rs-delete-confirm').disabled) return;
        withBusy($('rs-delete-confirm'),async () => {
          const next = structuredClone(context.config); next.rule_sets = next.rule_sets.filter(set => set.id !== context.id);
          try { await save(next); if(sameSession(context.generation) && deletingSet === context) $('rule-set-delete-dialog').close(); }
          catch(error) { if(sameSession(context.generation) && deletingSet === context) { $('rs-delete-error').textContent = errorText(error); $('rs-delete-error').dataset.errorSource = error.source || ''; if(error.status === 409) $('rs-delete-reload').hidden = false; } }
        });
      });
      for(const [id,dialogID] of [['group-delete-reload','group-delete-dialog'],['rs-delete-reload','rule-set-delete-dialog']]) $(id).addEventListener('click',() => withBusy($(id),async () => { const generation = getSession().generation; try { const next = await api('/config'); if(!sameSession(generation)) return; publishConfig(next); $(dialogID).close(); $(id).hidden = true; } catch(error) { if(sameSession(generation)) notice(error,true); } }));
      $('rt-form').addEventListener('submit',event => { event.preventDefault(); runPreview(); });
      for(const id of ['rt-host','rt-port','rt-source','rt-inbound','rt-family']) { $(id).addEventListener('input',invalidatePreview); $(id).addEventListener('change',invalidatePreview); }
    }
    return {bind,render,reset,configChanged,createDraft,reviewDraft,saveDraft};
  }
  globalThis.RillwayEditors = {create};
})();
