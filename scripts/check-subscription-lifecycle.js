// Playwright snippet: run from a console URL serving the current bundle.
// A separate tab intercepts every API request and every write; no financial mutation reaches a server.
async (page) => {
 const test=await page.context().newPage(),writes=[];
 const plans=[{id:'base',name:'Base',amount:1000,currency:'brl'},{id:'pro',name:'Pro',amount:2000,currency:'brl'},{id:'usd',name:'USD',amount:2000,currency:'usd'}];
 let subscription={id:'fixture-sub',customer_id:'fixture-customer',plan_id:'base',provider:'stripe',status:'active',billing_credit:200,paused_at:null,pending_change:null};
 const charges=[{id:'fixture-charge',customer_id:'fixture-customer',amount:0,currency:'usd',status:'paid',kind:'credit_settlement',billing_credit_used:1234,method:'card',installments:1}];
 await test.route('**/*',async route=>{
  const request=route.request(),path=new URL(request.url()).pathname;
  if(request.method()!=='GET'||path.startsWith('/console/api/')){
   let body={data:[]};
   if(request.method()!=='GET'){
    writes.push({path,body:request.postDataJSON(),key:request.headers()['idempotency-key']});
    body=path.endsWith('/preview')?{plan_id:'pro',old_plan_id:'base',amount_due:1500,unused_credit:300,billing_credit_after:0,credits_to_grant:-2,currency:'brl',new_period_end:'2026-12-01'}:{status:'pending',change_id:'fixture-change'};
   }else if(path==='/console/api/me')body={is_admin:true};
   else if(path==='/console/api/projects')body={data:[{id:'fixture-one',name:'Fixture One'},{id:'fixture-two',name:'Fixture Two'}]};
   else if(path.endsWith('/plans'))body={data:plans};
   else if(path.endsWith('/subscriptions'))body={data:[subscription]};
   else if(path.endsWith('/customers'))body={data:[{id:'fixture-customer',external_id:'Fixture customer'}]};
   else if(path.endsWith('/charges'))body={data:charges};
   else if(path.endsWith('/history'))body={charge:charges[0],attempts:[],events:[],webhooks:[]};
   return route.fulfill({contentType:'application/json',body:JSON.stringify(body)});
  }
  if(path==='/console/'||path==='/console'||path.startsWith('/console/assets/'))return route.fallback();
  return route.abort();
 });
 const assert=(value,label)=>{if(!value)throw Error(label);};
 try{
  await test.goto(new URL('/console',page.url()).href);
  await test.getByRole('button',{name:'Assinaturas',exact:true}).click();
  await test.getByRole('button',{name:'Alterar plano',exact:true}).click();
  assert(await test.locator('dialog[open] option').count()===1,'Incompatible currency available');
  await test.getByRole('button',{name:'Revisar valores',exact:true}).click();
  await test.getByRole('button',{name:'Confirmar alteração',exact:true}).waitFor();
  assert(writes.length===1&&(await test.getByRole('dialog').innerText()).includes('-2 créditos'),'Preview applied a mutation or lost signed credits');
  await test.evaluate(()=>{const form=document.querySelector('dialog[open] form');form.requestSubmit();form.requestSubmit();});
  await test.waitForFunction(()=>!document.querySelector('dialog[open]')&&!loading);
  assert(writes.length===2&&writes[1].body.max_amount===1500&&writes.every(w=>w.key),'Duplicate submission, missing cap or missing idempotency key');
  await test.getByRole('button',{name:'Pausar',exact:true}).click();
  await test.getByRole('dialog').getByRole('button',{name:'Cancelar',exact:true}).click();
  assert(writes.length===2,'Cancellation issued a write');
  await test.evaluate(()=>{subscriptionDialog(data.subscriptions[0],'pause');tenant='fixture-two';});
  await test.getByRole('dialog').getByRole('button',{name:'Confirmar',exact:true}).click();
  assert(writes.length===2,'Tenant change issued a write');
  await test.getByRole('dialog').getByRole('button',{name:'Cancelar',exact:true}).click();
  subscription={...subscription,pending_change:{status:'review'}};
  await test.getByRole('button',{name:'Atualizar',exact:true}).click();
  await test.getByText('Alteração requer revisão do operador',{exact:true}).waitFor();
  assert(!await test.getByRole('button',{name:'Pausar',exact:true}).count()&&!await test.getByRole('button',{name:'Cancelar',exact:true}).count(),'Pending review permits a conflicting action');
  await test.getByRole('button',{name:'Cobranças',exact:true}).click();
  await test.getByText('Quitada com crédito monetário',{exact:true}).waitFor();
  assert((await test.locator('#content').innerText()).includes('US$'),'Credit settlement lost its currency');
  await test.getByRole('button',{name:'Ver detalhes',exact:true}).click();
  await test.getByRole('heading',{name:'Detalhes da cobrança',exact:true}).waitFor();
  await test.getByText('Quitada com crédito monetário',{exact:true}).waitFor();
  return {mockedOnly:true,compatibleCurrency:true,explicitPreview:true,signedUsageCredits:true,doubleSubmitGuard:true,maxAmountCap:true,idempotency:true,cancelAndTenantGuards:true,pendingReviewGuard:true,creditSettlementLabelsAndCurrency:true};
 }finally{await test.close();}
}
