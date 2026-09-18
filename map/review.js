const elements = {
  loading: document.querySelector('#loadingState'),
  empty: document.querySelector('#emptyState'),
  error: document.querySelector('#errorState'),
  errorMessage: document.querySelector('#errorMessage'),
  workspace: document.querySelector('#reviewWorkspace'),
  pendingTasks: document.querySelector('#pendingTasks'),
  completedTasks: document.querySelector('#completedTasks'),
  restaurantLink: document.querySelector('#restaurantLink'),
  photoCounter: document.querySelector('#photoCounter'),
  menuImage: document.querySelector('#menuImage'),
  structuredEvidence: document.querySelector('#structuredEvidence'),
  photoStrip: document.querySelector('#photoStrip'),
  evidenceTitle: document.querySelector('#evidenceTitle'),
  evidenceList: document.querySelector('#evidenceList'),
  categoryCount: document.querySelector('#categoryCount'),
  reviewProgress: document.querySelector('#reviewProgress'),
  categoryList: document.querySelector('#categoryList'),
  missingCategory: document.querySelector('#missingCategory'),
  addMissingCategory: document.querySelector('#addMissingCategory'),
  additionList: document.querySelector('#additionList'),
  proposalName: document.querySelector('#proposalName'),
  proposalParent: document.querySelector('#proposalParent'),
  proposalExplanation: document.querySelector('#proposalExplanation'),
  addProposal: document.querySelector('#addProposal'),
  proposalList: document.querySelector('#proposalList'),
  form: document.querySelector('#reviewForm'),
  note: document.querySelector('#reviewNote'),
  save: document.querySelector('#saveReview'),
  submitHint: document.querySelector('#submitHint'),
  toast: document.querySelector('#toast'),
};

const state = {
  task: null,
  taxonomy: [],
  decisions: new Map(),
  additions: new Set(),
  proposals: [],
  selectedCandidateID: null,
  selectedPhoto: '',
  saving: false,
};

function showView(name) {
  elements.loading.hidden = name !== 'loading';
  elements.empty.hidden = name !== 'empty';
  elements.error.hidden = name !== 'error';
  elements.workspace.hidden = name !== 'workspace';
}

async function readError(response) {
  try {
    const body = await response.json();
    return body.error || `Request failed (${response.status})`;
  } catch {
    return `Request failed (${response.status})`;
  }
}

function foodTypeLabel(foodType) {
  if (!foodType) return 'Unknown category';
  const name = foodType.name_en || foodType.name_ja || foodType.slug;
  return foodType.parent_name_en ? `${foodType.parent_name_en} › ${name}` : name;
}

function foodTypeLocalLabel(foodType) {
  const japanese = foodType.parent_name_ja
    ? `${foodType.parent_name_ja} › ${foodType.name_ja}`
    : foodType.name_ja;
  const chinese = foodType.parent_name_zh
    ? `${foodType.parent_name_zh} › ${foodType.name_zh}`
    : foodType.name_zh;
  return [japanese, chinese].filter(Boolean).join(' · ');
}

function foodTypeByID(id) {
  return state.taxonomy.find((foodType) => foodType.id === Number(id));
}

function appendFoodTypeOptions(select, { blankLabel = 'Choose a category…', exclude = new Set() } = {}) {
  select.replaceChildren();
  const blank = document.createElement('option');
  blank.value = '';
  blank.textContent = blankLabel;
  select.append(blank);
  for (const foodType of state.taxonomy) {
    if (exclude.has(foodType.id)) continue;
    const option = document.createElement('option');
    option.value = String(foodType.id);
    option.textContent = `${foodTypeLabel(foodType)} · ${foodType.name_ja}`;
    select.append(option);
  }
}

async function loadTaxonomy() {
  const response = await fetch('/api/food-types', { headers: { Accept: 'application/json' } });
  if (!response.ok) throw new Error(await readError(response));
  state.taxonomy = await response.json();
  appendFoodTypeOptions(elements.missingCategory);
  appendFoodTypeOptions(elements.proposalParent, { blankLabel: 'No parent selected' });
}

async function refreshStats() {
  try {
    const response = await fetch('/api/food-category-reviews/stats');
    if (!response.ok) return;
    const stats = await response.json();
    elements.pendingTasks.textContent = stats.pending_tasks.toLocaleString();
    elements.completedTasks.textContent = stats.completed_tasks.toLocaleString();
  } catch {
    // Queue statistics should not prevent category review.
  }
}

async function loadNextTask() {
  showView('loading');
  state.task = null;
  state.decisions.clear();
  state.additions.clear();
  state.proposals = [];
  state.selectedCandidateID = null;
  state.selectedPhoto = '';
  elements.note.value = '';
  elements.proposalName.value = '';
  elements.proposalParent.value = '';
  elements.proposalExplanation.value = '';
  renderAdditions();
  renderProposals();

  try {
    const response = await fetch('/api/food-category-reviews/next', {
      headers: { Accept: 'application/json' },
    });
    if (response.status === 204) {
      showView('empty');
      await refreshStats();
      return;
    }
    if (!response.ok) throw new Error(await readError(response));
    const task = await response.json();
    if (!task || !Array.isArray(task.candidates)) {
      throw new Error('The next restaurant has an invalid category-review payload.');
    }
    state.task = task;
    renderTask(task);
    showView('workspace');
    await refreshStats();
  } catch (error) {
    elements.errorMessage.textContent = error.message || 'Please try again.';
    showView('error');
  }
}

function selectPhoto(imageURL) {
  if (!imageURL) return;
  state.selectedPhoto = imageURL;
  elements.menuImage.src = imageURL;
  elements.menuImage.hidden = false;
  elements.structuredEvidence.hidden = true;
  elements.photoStrip.querySelectorAll('.photo-thumb').forEach((button) => {
    button.classList.toggle('active', button.dataset.imageUrl === imageURL);
  });
  const index = (state.task?.photos || []).indexOf(imageURL);
  elements.photoCounter.textContent = index >= 0
    ? `Photo ${index + 1} of ${state.task.photos.length}`
    : '';
}

function renderPhotos(photos) {
  elements.photoStrip.replaceChildren();
  if (!photos.length) {
    elements.menuImage.hidden = true;
    elements.menuImage.removeAttribute('src');
    elements.structuredEvidence.hidden = false;
    elements.photoCounter.textContent = 'Text evidence';
    return;
  }
  photos.forEach((imageURL, index) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'photo-thumb';
    button.dataset.imageUrl = imageURL;
    button.setAttribute('aria-label', `Show menu photo ${index + 1}`);
    const image = document.createElement('img');
    image.src = imageURL;
    image.alt = '';
    image.loading = 'lazy';
    image.referrerPolicy = 'no-referrer';
    button.append(image);
    button.addEventListener('click', () => selectPhoto(imageURL));
    elements.photoStrip.append(button);
  });
  selectPhoto(photos[0]);
}

function showEvidence(candidate) {
  state.selectedCandidateID = candidate.id;
  elements.categoryList.querySelectorAll('.category-card').forEach((card) => {
    card.classList.toggle('selected', Number(card.dataset.candidateId) === candidate.id);
  });
  elements.evidenceTitle.textContent = foodTypeLabel(candidate.suggested_food_type);
  elements.evidenceList.replaceChildren();
  for (const evidence of candidate.evidence || []) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'evidence-row';
    const text = document.createElement('strong');
    text.textContent = evidence.text;
    const source = document.createElement('span');
    source.textContent = evidence.image_url ? 'Show supporting photo' : 'Structured menu text';
    button.append(text, source);
    if (evidence.image_url) {
      button.addEventListener('click', () => selectPhoto(evidence.image_url));
    } else {
      button.disabled = true;
    }
    elements.evidenceList.append(button);
  }
  const firstPhotoEvidence = (candidate.evidence || []).find((evidence) => evidence.image_url);
  if (firstPhotoEvidence) selectPhoto(firstPhotoEvidence.image_url);
}

function makeAction(label, className) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = `choice ${className}`;
  button.textContent = label;
  button.setAttribute('aria-pressed', 'false');
  return button;
}

function setDecision(candidateID, decision, foodTypeID = 0) {
  state.decisions.set(candidateID, { decision, foodTypeID });
  paintCandidate(candidateID);
  updateProgress();
}

function paintCandidate(candidateID) {
  const card = elements.categoryList.querySelector(`[data-candidate-id="${candidateID}"]`);
  if (!card) return;
  const value = state.decisions.get(candidateID);
  const decision = value?.decision || 'pending';
  card.classList.remove('approved', 'changed', 'rejected');
  if (decision !== 'pending') card.classList.add(decision);
  card.querySelectorAll('.choice').forEach((button) => {
    button.classList.toggle('active', button.dataset.decision === decision);
    button.setAttribute('aria-pressed', String(button.dataset.decision === decision));
  });
  const select = card.querySelector('.change-select');
  select.hidden = decision !== 'changed';
  if (decision === 'changed' && value.foodTypeID) {
    select.value = String(value.foodTypeID);
  }
}

function renderTask(task) {
  elements.restaurantLink.textContent = task.restaurant_name || `Restaurant #${task.restaurant_id}`;
  elements.restaurantLink.href = task.restaurant_url || '#';
  elements.categoryCount.textContent = `${task.candidates.length} ${task.candidates.length === 1 ? 'category' : 'categories'}`;
  appendFoodTypeOptions(elements.missingCategory, {
    exclude: new Set(task.candidates.map((candidate) => candidate.suggested_food_type.id)),
  });
  renderPhotos(task.photos || []);
  elements.categoryList.replaceChildren();

  task.candidates.forEach((candidate, index) => {
    const foodType = candidate.suggested_food_type;
    const card = document.createElement('article');
    card.className = 'category-card';
    card.dataset.candidateId = String(candidate.id);

    const heading = document.createElement('button');
    heading.type = 'button';
    heading.className = 'category-heading';
    const number = document.createElement('span');
    number.className = 'item-number';
    number.textContent = String(index + 1).padStart(2, '0');
    const names = document.createElement('span');
    names.className = 'category-names';
    const english = document.createElement('strong');
    english.textContent = foodTypeLabel(foodType);
    const local = document.createElement('span');
    local.textContent = foodTypeLocalLabel(foodType);
    names.append(english, local);
    const evidenceCount = document.createElement('span');
    evidenceCount.className = 'evidence-count';
    evidenceCount.textContent = `${(candidate.evidence || []).length} evidence`;
    heading.append(number, names, evidenceCount);
    heading.addEventListener('click', () => showEvidence(candidate));

    const select = document.createElement('select');
    select.className = 'change-select';
    select.hidden = true;
    appendFoodTypeOptions(select, { exclude: new Set([foodType.id]) });
    select.addEventListener('change', () => {
      const foodTypeID = Number(select.value);
      if (foodTypeID) setDecision(candidate.id, 'changed', foodTypeID);
      else {
        state.decisions.delete(candidate.id);
        paintCandidate(candidate.id);
        updateProgress();
      }
    });

    const actions = document.createElement('div');
    actions.className = 'category-actions';
    const keep = makeAction('Keep', 'keep');
    const change = makeAction('Change', 'change');
    const remove = makeAction('Remove', 'remove');
    keep.dataset.decision = 'approved';
    change.dataset.decision = 'changed';
    remove.dataset.decision = 'rejected';
    keep.addEventListener('click', () => setDecision(candidate.id, 'approved'));
    change.addEventListener('click', () => {
      const current = state.decisions.get(candidate.id);
      setDecision(candidate.id, 'changed', current?.foodTypeID || 0);
      select.focus();
    });
    remove.addEventListener('click', () => setDecision(candidate.id, 'rejected'));
    actions.append(keep, change, remove);

    card.append(heading, select, actions);
    elements.categoryList.append(card);
  });

  if (task.candidates[0]) {
    showEvidence(task.candidates[0]);
  } else {
    const empty = document.createElement('p');
    empty.className = 'muted empty-candidates';
    empty.textContent = 'The model found no categories. Inspect the photos, add any missing categories, or save to confirm that none apply.';
    elements.categoryList.append(empty);
    elements.evidenceTitle.textContent = 'No suggested categories';
    elements.evidenceList.innerHTML = '<p class="muted">Inspect each menu photo before completing this restaurant.</p>';
  }
  updateProgress();
}

function updateProgress() {
  const total = state.task?.candidates.length || 0;
  const checked = [...state.decisions.values()].filter((value) => (
    value.decision !== 'changed' || value.foodTypeID > 0
  )).length;
  const complete = Boolean(state.task) && checked === total;
  elements.reviewProgress.textContent = `${checked} / ${total} checked`;
  elements.reviewProgress.classList.toggle('complete', complete);
  elements.save.disabled = !complete || state.saving;
  elements.submitHint.textContent = complete
    ? (total === 0 ? 'Ready to confirm the review.' : 'Ready to publish confirmed food types.')
    : `Review ${total - checked} more ${total - checked === 1 ? 'category' : 'categories'} to continue.`;
}

function applyToAll(decision) {
  if (!state.task) return;
  for (const candidate of state.task.candidates) {
    setDecision(candidate.id, decision);
  }
}

function renderAdditions() {
  elements.additionList.replaceChildren();
  for (const id of state.additions) {
    const foodType = foodTypeByID(id);
    if (!foodType) continue;
    const tag = document.createElement('span');
    tag.className = 'tag';
    tag.textContent = foodTypeLabel(foodType);
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.textContent = '×';
    remove.setAttribute('aria-label', `Remove ${foodTypeLabel(foodType)}`);
    remove.addEventListener('click', () => {
      state.additions.delete(id);
      renderAdditions();
    });
    tag.append(remove);
    elements.additionList.append(tag);
  }
}

function addMissingCategory() {
  const id = Number(elements.missingCategory.value);
  if (!id) return;
  state.additions.add(id);
  elements.missingCategory.value = '';
  renderAdditions();
}

function renderProposals() {
  elements.proposalList.replaceChildren();
  state.proposals.forEach((proposal, index) => {
    const row = document.createElement('div');
    row.className = 'proposal-row';
    const text = document.createElement('span');
    text.textContent = proposal.proposed_name;
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.textContent = 'Remove';
    remove.addEventListener('click', () => {
      state.proposals.splice(index, 1);
      renderProposals();
    });
    row.append(text, remove);
    elements.proposalList.append(row);
  });
}

function addProposal() {
  const proposedName = elements.proposalName.value.trim();
  if (!proposedName) {
    showToast('Enter a proposed category name first.');
    return;
  }
  const selectedCandidate = state.task?.candidates.find(
    (candidate) => candidate.id === state.selectedCandidateID,
  );
  const evidence = (selectedCandidate?.evidence || []).map((item) => ({
    image_url: item.image_url || '',
    source_page_url: item.source_page_url || '',
    text: item.text,
    block_ids: item.block_ids || [],
  }));
  state.proposals.push({
    proposed_name: proposedName,
    parent_food_type_id: Number(elements.proposalParent.value) || 0,
    explanation: elements.proposalExplanation.value.trim(),
    evidence,
  });
  elements.proposalName.value = '';
  elements.proposalParent.value = '';
  elements.proposalExplanation.value = '';
  renderProposals();
}

async function saveReview(event) {
  event.preventDefault();
  if (!state.task || state.saving || elements.save.disabled) return;
  const candidates = state.task.candidates.map((candidate) => {
    const value = state.decisions.get(candidate.id);
    return {
      id: candidate.id,
      decision: value.decision,
      food_type_id: value.foodTypeID || 0,
    };
  });
  const additions = [...state.additions].map((foodTypeID) => ({ food_type_id: foodTypeID }));

  state.saving = true;
  elements.save.querySelector('.button-label').textContent = 'Saving…';
  updateProgress();
  try {
    const response = await fetch(`/api/food-category-reviews/${state.task.id}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({
        candidates,
        additions,
        proposals: state.proposals,
        note: elements.note.value.trim(),
      }),
    });
    if (!response.ok) throw new Error(await readError(response));
    showToast('Review saved. Confirmed food types are now searchable.');
    await loadNextTask();
  } catch (error) {
    showToast(error.message || 'Could not save this review.');
  } finally {
    state.saving = false;
    elements.save.querySelector('.button-label').textContent = 'Save & next';
    updateProgress();
  }
}

let toastTimer;
function showToast(message) {
  window.clearTimeout(toastTimer);
  elements.toast.textContent = message;
  elements.toast.hidden = false;
  toastTimer = window.setTimeout(() => { elements.toast.hidden = true; }, 4000);
}

document.querySelector('#approveAll').addEventListener('click', () => applyToAll('approved'));
document.querySelector('#rejectAll').addEventListener('click', () => applyToAll('rejected'));
document.querySelector('#refreshEmpty').addEventListener('click', loadNextTask);
document.querySelector('#retryButton').addEventListener('click', loadNextTask);
elements.addMissingCategory.addEventListener('click', addMissingCategory);
elements.addProposal.addEventListener('click', addProposal);
elements.form.addEventListener('submit', saveReview);

document.addEventListener('keydown', (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
    event.preventDefault();
    if (!elements.save.disabled) elements.form.requestSubmit();
  }
});

async function start() {
  try {
    await loadTaxonomy();
    await loadNextTask();
  } catch (error) {
    elements.errorMessage.textContent = error.message || 'Please try again.';
    showView('error');
  }
}

start();
